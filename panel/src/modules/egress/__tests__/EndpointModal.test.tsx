import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, test, vi } from "vitest";
import i18n from "@/i18n";
import type { EgressEndpoint } from "@/lib/http/apis/egress";
import { EndpointModal } from "@/modules/egress/EndpointModal";

const endpoint: EgressEndpoint = {
  id: "hk-socks",
  name: "Hong Kong",
  protocol: "socks5",
  host: "10.77.0.2",
  port: 1080,
  enabled: true,
  hasCredentials: true,
  username: "relay",
  status: "healthy",
  publicIp: "198.51.100.9",
  expectedPublicIp: "203.0.113.8",
  eligible: true,
  runtimeReady: true,
  eligibility: {
    selectable: true,
    eligible: true,
    runtimeReady: true,
    healthFresh: true,
    publicIpMatches: true,
    duplicatePublicIp: false,
    reasonCodes: [],
  },
};

describe("EndpointModal", () => {
  beforeEach(async () => {
    await i18n.changeLanguage("en");
  });

  test("contains only standalone endpoint fields", () => {
    render(
      <EndpointModal
        open
        endpoint={endpoint}
        saving={false}
        onClose={vi.fn()}
        onSave={vi.fn()}
      />,
    );

    expect(screen.getByLabelText("Protocol")).toBeInTheDocument();
    expect(screen.getByLabelText("Host")).toBeInTheDocument();
    expect(screen.getByLabelText("Port")).toBeInTheDocument();
  });

  test("offers only SOCKS5 and HTTP CONNECT protocols", async () => {
    const user = userEvent.setup();
    render(
      <EndpointModal
        open
        endpoint={null}
        saving={false}
        onClose={vi.fn()}
        onSave={vi.fn()}
      />,
    );

    await user.click(screen.getByRole("combobox", { name: "Protocol" }));

    expect(screen.getByRole("option", { name: "SOCKS5" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "HTTP CONNECT" })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: "HTTPS CONNECT" })).not.toBeInTheDocument();
    expect(screen.getAllByRole("option")).toHaveLength(2);
  });

  test("requires a valid expected IPv4 or IPv6 address for enabled endpoints", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn();
    render(
      <EndpointModal
        open
        endpoint={endpoint}
        saving={false}
        onClose={vi.fn()}
        onSave={onSave}
      />,
    );

    const input = screen.getByLabelText("Expected public IP");
    await user.clear(input);
    await user.click(screen.getByRole("button", { name: "Save endpoint" }));
    expect(screen.getByText("Expected public IP is required for an enabled endpoint.")).toBeVisible();
    expect(onSave).not.toHaveBeenCalled();

    await user.type(input, "not-an-ip");
    await user.click(screen.getByRole("button", { name: "Save endpoint" }));
    expect(screen.getByText("Enter a valid IPv4 or IPv6 address.")).toBeVisible();

    await user.clear(input);
    await user.type(input, "2001:db8::8");
    await user.click(screen.getByRole("button", { name: "Save endpoint" }));
    expect(onSave).toHaveBeenCalledWith(expect.objectContaining({ expectedPublicIp: "2001:db8::8" }));
  });

  test("never prefills the stored password and can explicitly clear credentials", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn();
    render(
      <EndpointModal
        open
        endpoint={endpoint}
        saving={false}
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
    expect(onSave).toHaveBeenLastCalledWith(expect.objectContaining({ clearCredentials: true }));
  });
});
