import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import { Tabs, TabsList, TabsTrigger } from "../Tabs";

describe("TabsList", () => {
  test("contains horizontal overscroll so parent/viewport does not rubber-band", () => {
    render(
      <Tabs value="a" onValueChange={() => {}}>
        <TabsList>
          <TabsTrigger value="a">Alpha</TabsTrigger>
          <TabsTrigger value="b">Beta</TabsTrigger>
        </TabsList>
      </Tabs>,
    );

    const tablist = screen.getByRole("tablist");
    expect(tablist).toHaveClass("overflow-x-auto");
    expect(tablist).toHaveClass("overscroll-x-contain");
  });
});
