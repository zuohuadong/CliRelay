import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import i18n from "@/i18n";
import { ModelsTabContent } from "@/modules/apikey-lookup/components/ModelsTabContent";
import { ThemeProvider } from "@/modules/ui/ThemeProvider";
import { ToastProvider } from "@/modules/ui/ToastProvider";

describe("ModelsTabContent", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  test("does not render the CC Switch import action inside available models", async () => {
    await i18n.changeLanguage("en");

    render(
      <ThemeProvider>
        <ToastProvider>
          <ModelsTabContent
            models={["claude-sonnet-4-5", "gpt-5.3-codex", "gemini-2.5-pro"]}
            loading={false}
            error={null}
            searchFilter=""
            onSearchChange={() => {}}
          />
        </ToastProvider>
      </ThemeProvider>,
    );

    expect(screen.queryByText(/import to cc switch/i)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /import codex/i })).not.toBeInTheDocument();
  });
});
