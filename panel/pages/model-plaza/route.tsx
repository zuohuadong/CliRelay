import { preloadablePage } from "../preloadablePage";

const { Page: ModelPlazaPage, preload: preloadModelPlazaPage } = preloadablePage(() =>
  import("./ModelPlazaPage").then((m) => ({ default: m.ModelPlazaPage })),
);

export const modelPlazaRoute = {
  path: "/models/plaza",
  component: "model-plaza",
  element: <ModelPlazaPage />,
  auth: true,
  layout: "dashboard",
  nav: { labelKey: "nav.modelPlaza" },
  redirects: [
    { from: "/model-plaza", to: "/models/plaza" },
    { from: "/models/available", to: "/models/plaza" },
  ],
  requiredPermission: "system.status.read",
  preload: preloadModelPlazaPage,
};
