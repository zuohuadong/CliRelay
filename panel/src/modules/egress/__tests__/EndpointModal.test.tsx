import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, test, vi } from "vitest";
import i18n from "@/i18n";
import type { EgressEndpoint, EgressNode } from "@/lib/http/apis/egress";
import { EndpointModal } from "@/modules/egress/EndpointModal";

const nodes: EgressNode[] = [
  {
    id: "node-1",
    name: "egress-hk",
    ipAddresses: ["100.64.0.2"],
    online: true,
    fresh: true,
    syncAgeSeconds: 0,
    tags: ["tag:clirelay-egress"],
  },
];

const endpoint: EgressEndpoint = {
  id: "hk-socks",
  nodeId: "node-1",
  name: "Hong Kong",
  protocol: "socks5",
  host: "100.64.0.2",
  port: 1080,
  enabled: true,
  isLocal: false,
  hasCredentials: true,
  username: "relay",
  status: "healthy",
  publicIp: "198.51.100.9",
  expectedPublicIp: "203.0.113.8",
};

describe("EndpointModal", () => {
  beforeEach(async () => {
    await i18n.changeLanguage("en");
  });

  test("requires a valid expected IPv4 or IPv6 address for enabled endpoints", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn();
    render(
      <EndpointModal
        open
        endpoint={endpoint}
        nodes={nodes}
        saving={false}
        localEndpointEnabled={false}
        onClose={vi.fn()}
        onSave={onSave}
      />,
    );

    const input = screen.getByLabelText("Expected public IP");
    await user.clear(input);
    await user.click(screen.getByRole("button", { name: "Save endpoint" }));
    expect(
      screen.getByText("Expected public IP is required for an enabled endpoint."),
    ).toBeVisible();
    expect(onSave).not.toHaveBeenCalled();

    await user.type(input, "not-an-ip");
    await user.click(screen.getByRole("button", { name: "Save endpoint" }));
    expect(screen.getByText("Enter a valid IPv4 or IPv6 address.")).toBeVisible();
    expect(onSave).not.toHaveBeenCalled();

    await user.clear(input);
    await user.type(input, "2001:db8::8");
    await user.click(screen.getByRole("button", { name: "Save endpoint" }));
    expect(onSave).toHaveBeenCalledWith(
      expect.objectContaining({ expectedPublicIp: "2001:db8::8" }),
    );
  });

  test("shows the expected address on create and restores it on edit without copying observed IP", () => {
    const { rerender } = render(
      <EndpointModal
        open
        endpoint={null}
        nodes={nodes}
        saving={false}
        localEndpointEnabled={false}
        onClose={vi.fn()}
        onSave={vi.fn()}
      />,
    );

    expect(screen.getByLabelText("Expected public IP")).toHaveValue("");

    rerender(
      <EndpointModal
        open
        endpoint={endpoint}
        nodes={nodes}
        saving={false}
        localEndpointEnabled={false}
        onClose={vi.fn()}
        onSave={vi.fn()}
      />,
    );

    expect(screen.getByLabelText("Expected public IP")).toHaveValue("203.0.113.8");
    expect(screen.getByLabelText("Expected public IP")).not.toHaveValue("198.51.100.9");
  });

  test("keeps blank password updates unambiguous and explicitly clears stored credentials", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn();
    render(
      <EndpointModal
        open
        endpoint={endpoint}
        nodes={nodes}
        saving={false}
        localEndpointEnabled={false}
        onClose={vi.fn()}
        onSave={onSave}
      />,
    );

    expect(screen.getByLabelText("Password")).toHaveValue("");
    expect(screen.getByText("Leave blank to keep the existing password.")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Save endpoint" }));
    expect(onSave).toHaveBeenLastCalledWith(
      expect.not.objectContaining({ password: expect.anything(), clearCredentials: expect.anything() }),
    );

    await user.click(screen.getByRole("switch", { name: "Clear stored credentials" }));
    await user.click(screen.getByRole("button", { name: "Save endpoint" }));
    expect(onSave).toHaveBeenLastCalledWith(
      expect.objectContaining({ clearCredentials: true }),
    );
  });
});
