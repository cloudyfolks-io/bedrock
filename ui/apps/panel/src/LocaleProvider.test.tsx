import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Tab, TabList, TabPanel, Tabs } from "@bedrock/design";
import { describe, expect, it } from "vitest";
import { useLocale } from "./LocaleProvider";
import { renderPanel } from "./test/renderPanel";

function Probe() {
  const { setLocale } = useLocale();
  return (
    <>
      <button type="button" onClick={() => setLocale("ar")}>
        to-ar
      </button>
      <Tabs value="a" onValueChange={() => {}}>
        <TabList>
          <Tab value="a">One</Tab>
          <Tab value="b">Two</Tab>
        </TabList>
        <TabPanel value="a">a</TabPanel>
      </Tabs>
    </>
  );
}

describe("LocaleProvider", () => {
  it("feeds the Base UI direction so ArrowLeft goes to the next tab in Arabic", async () => {
    const user = userEvent.setup();
    renderPanel(<Probe />);
    await user.click(screen.getByRole("button", { name: "to-ar" }));
    expect(document.documentElement.dir).toBe("rtl");
    await user.click(screen.getByRole("tab", { name: "One" }));
    await user.keyboard("{ArrowLeft}");
    expect(document.activeElement).toBe(screen.getByRole("tab", { name: "Two" }));
  });

  it("starts in the requested locale", () => {
    renderPanel(<Probe />, { locale: "fa" });
    expect(document.documentElement.dir).toBe("rtl");
    expect(document.documentElement.lang).toBe("fa");
  });
});
