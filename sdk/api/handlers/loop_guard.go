package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/diagnostics"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/loopdetect"
	log "github.com/sirupsen/logrus"
)

// LoopGuardGate resolves the loop protection settings for a request.
type LoopGuardGate func() *loopdetect.Policy

const LoopGuardGateContextKey = "cliproxy.loopGuardGate"

func LoopGuardGateFromGin(c *gin.Context) LoopGuardGate {
	if c == nil {
		return nil
	}
	value, ok := c.Get(LoopGuardGateContextKey)
	if !ok {
		return nil
	}
	gate, _ := value.(LoopGuardGate)
	return gate
}

func loopGuardPolicy(ctx context.Context) *loopdetect.Policy {
	gate := LoopGuardGateFromGin(ginContextFrom(ctx))
	if gate == nil {
		return nil
	}
	return gate()
}

const LoopGuardHeader = "X-CliRelay-Loop-Guard"

func loopGuardHeaders(kind loopdetect.Kind) http.Header {
	headers := make(http.Header, 1)
	headers.Set(LoopGuardHeader, string(kind))
	return headers
}

func toolLoopReply(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string, stream bool) (loopdetect.LocalReply, bool) {
	if strings.Contains(alt, "/") || len(rawJSON) == 0 {
		return loopdetect.LocalReply{}, false
	}
	policy := loopGuardPolicy(ctx)
	if !policy.ToolOn() {
		return loopdetect.LocalReply{}, false
	}
	protocol, ok := loopdetect.ProtocolOf(handlerType)
	if !ok {
		return loopdetect.LocalReply{}, false
	}
	loop, ok := detectToolLoop(protocol, rawJSON, *policy.Tool)
	if !ok {
		return loopdetect.LocalReply{}, false
	}
	reply := loopdetect.LocalReply{
		Protocol: protocol,
		Model:    modelName,
		Text:     loop.Notice(),
		ID:       strings.ReplaceAll(uuid.NewString(), "-", "")[:24],
		Created:  time.Now().Unix(),
	}
	if policy.Observer != nil {
		policy.Observer.ToolLoopAnswered(ctx, loopdetect.ToolAnswered{
			Limits: policy.Tool.Normalized(), Loop: loop, Model: modelName, Protocol: protocol, Stream: stream,
		})
	}
	// A tool loop is answered locally, before an executor creates a usage
	// reporter. Publish the request count and guard outcome directly so it is
	// visible in request logs just like an upstream execution.
	record := coreusage.Record{
		Model:       modelName,
		GuardKind:   string(loopdetect.KindToolLoop),
		Failed:      true,
		RequestedAt: time.Now(),
		Stream:      stream,
		Streaming:   stream,
		Generate:    coreusage.GenerateFlag(true),
	}
	snapshot := diagnostics.LoopGuardSnapshot{
		Kind:              diagnostics.LoopGuardToolLoop,
		Repeats:           policy.Tool.Normalized().Repeats,
		Turns:             loop.Turns,
		ConversationItems: loop.Items,
		ItemsRead:         loop.Read,
		Message:           loop.Notice(),
		Source:            policy.ToolSource,
	}
	snapshot.Calls = make([]diagnostics.LoopGuardCall, len(loop.Calls))
	for i, call := range loop.Calls {
		snapshot.Calls[i] = diagnostics.LoopGuardCall{Name: call.Name, Arguments: call.Arguments}
	}
	if payload, err := json.Marshal(map[string]any{"diagnostic": map[string]any{"loop_guard": snapshot}}); err == nil {
		record.DetailContent = string(payload)
	}
	coreusage.PublishRecord(ctx, record)
	return reply, true
}

func loopGuardDiagnostic(ctx context.Context) *diagnostics.RequestDiagnostic {
	if diagnostic := diagnostics.FromContext(ctx); diagnostic != nil {
		return diagnostic
	}
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok {
		return diagnostics.FromGin(ginCtx)
	}
	return nil
}

var toolLoopCheck = loopdetect.DetectToolLoop

func detectToolLoop(protocol loopdetect.Protocol, body []byte, limits loopdetect.ToolLimits) (loop loopdetect.ToolLoop, found bool) {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("loop guard: tool-loop check panicked and was skipped: %v", r)
			loop, found = loopdetect.ToolLoop{}, false
		}
	}()
	return toolLoopCheck(protocol, body, limits)
}

func localStream(chunks [][]byte) (<-chan []byte, <-chan *StreamErrorMessage) {
	data := make(chan []byte, len(chunks))
	for _, chunk := range chunks {
		data <- chunk
	}
	close(data)
	errs := make(chan *StreamErrorMessage)
	close(errs)
	return data, errs
}

type replyGuard struct {
	ctx    context.Context
	policy *loopdetect.Policy
	guard  *loopdetect.ReplyGuard
	mark   *loopdetect.Mark
	model  string
	off    bool
}

func newReplyGuard(ctx context.Context, model string) (context.Context, *replyGuard) {
	policy := loopGuardPolicy(ctx)
	if !policy.ReplyOn() {
		return ctx, nil
	}
	ctx, mark := loopdetect.WithMark(ctx)
	return ctx, &replyGuard{
		ctx: ctx, policy: policy, mark: mark, model: model,
		guard: loopdetect.NewReplyGuard(*policy.Reply),
	}
}

func (g *replyGuard) inspect(chunk []byte) *StreamErrorMessage {
	if g == nil || g.off {
		return nil
	}
	trip, ok := g.feed(chunk)
	if !ok {
		return nil
	}
	err := &loopdetect.ReplyLoopError{Trip: trip}
	g.mark.SetReply(loopdetect.KindReplyLoop, trip.Message(), g.policy.ReplySource, g.guard.Limits(), trip)
	if g.policy.Observer != nil {
		g.policy.Observer.ReplyCut(g.ctx, loopdetect.ReplyCut{Limits: g.guard.Limits(), Trip: trip, Model: g.model})
	}
	return &StreamErrorMessage{StatusCode: loopdetect.ReplyLoopStatus, Error: err}
}

func (g *replyGuard) feed(chunk []byte) (trip loopdetect.ReplyTrip, found bool) {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("loop guard: reply check panicked and was turned off for this stream: %v", r)
			g.off = true
			trip, found = loopdetect.ReplyTrip{}, false
		}
	}()
	return g.guard.Feed(chunk)
}
