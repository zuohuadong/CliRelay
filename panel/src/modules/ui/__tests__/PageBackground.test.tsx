import { render } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import { PageBackground } from "@/modules/ui/PageBackground";

describe("PageBackground", () => {
  test("keeps the login background static to avoid persistent flashing", () => {
    const { container } = render(
      <PageBackground variant="login">
        <main>Login</main>
      </PageBackground>,
    );

    expect(container.innerHTML).not.toMatch(/animate-\[(?:spin|pulse)_/);
  });
});
