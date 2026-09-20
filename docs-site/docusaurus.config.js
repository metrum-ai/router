// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

// @ts-check

const lightCodeTheme = require("prism-react-renderer").themes.github;
const darkCodeTheme = require("prism-react-renderer").themes.dracula;
const childProcess = require("child_process");

function latestRouterTag() {
  if (process.env.DOCS_LATEST_ROUTER_VERSION) {
    return process.env.DOCS_LATEST_ROUTER_VERSION;
  }
  try {
    return childProcess
      .execFileSync("git", ["describe", "--tags", "--abbrev=0"], {
        encoding: "utf8",
        stdio: ["ignore", "pipe", "ignore"],
      })
      .trim();
  } catch {
    return process.env.DOCS_ROUTER_VERSION || "dev";
  }
}

const routerVersion = process.env.DOCS_ROUTER_VERSION || "dev";
const routerBuildDate = process.env.DOCS_ROUTER_BUILD_DATE || "unknown";
const routerLatestVersion = latestRouterTag();

if (
  routerVersion !== "dev" &&
  routerLatestVersion !== "dev" &&
  routerLatestVersion !== routerVersion
) {
  console.warn(
    `Docs router version ${routerVersion} differs from latest release tag ${routerLatestVersion}.`
  );
}

/** @type {import('@docusaurus/types').Config} */
const config = {
  title: "Metrum AI Router",
  tagline: "An open-source LLM smart router",
  favicon: "img/favicon/favicon.ico",
  url: process.env.DOCS_SITE_URL || "https://llm-api.apps.metrum.ai",
  baseUrl: "/docs/",
  organizationName: "metrum-ai",
  projectName: "router",
  customFields: {
    routerVersion,
    routerBuildDate,
    routerLatestVersion,
    elevenLabsSupportAgentId: "agent_1001m04bhav8fck80kbbqwh69stq",
  },
  onBrokenLinks: "throw",
  markdown: {
    mermaid: true,
    hooks: {
      onBrokenMarkdownLinks: "throw",
    },
  },
  trailingSlash: false,

  i18n: {
    defaultLocale: "en",
    locales: ["en"],
  },

  presets: [
    [
      "classic",
      /** @type {import('@docusaurus/preset-classic').Options} */
      ({
        docs: {
          sidebarPath: require.resolve("./sidebars.js"),
          routeBasePath: "/",
          editUrl: undefined,
        },
        blog: false,
        theme: {
          customCss: require.resolve("./src/css/custom.css"),
        },
      }),
    ],
  ],
  themes: ["@docusaurus/theme-mermaid"],

  themeConfig:
    /** @type {import('@docusaurus/preset-classic').ThemeConfig} */
    ({
      image: "img/metrum_logo_white_new.png",
      colorMode: {
        defaultMode: "dark",
        disableSwitch: false,
        respectPrefersColorScheme: false,
      },
      navbar: {
        title: "Metrum AI Router",
        logo: {
          alt: "Metrum AI",
          src: "img/metrum_logo_white_new.png",
          srcDark: "img/metrum_logo_white_new.png",
        },
        items: [
          { to: "/overview", label: "Docs", position: "left" },
          { to: "/faq", label: "FAQ", position: "left" },
          { to: "/installation/", label: "Install", position: "left" },
          { to: "/evaluation/evaluate-smart-router", label: "Evaluate", position: "left" },
          { to: "/solution-brief", label: "Solution Brief", position: "left" },
          { to: "/evaluation/harbor-case-study", label: "Case Study", position: "left" },
          { to: "/privacy", label: "Privacy", position: "right" },
          { href: "https://github.com/metrum-ai/router", label: "GitHub", position: "right" },
        ],
      },
      footer: {
        style: "dark",
        links: [
          {
            title: "Product",
            items: [
              { label: "Overview", to: "/overview" },
              { label: "Concepts", to: "/concepts" },
              { label: "Glossary", to: "/concepts/glossary" },
              { label: "FAQ", to: "/faq" },
              { label: "Installation", to: "/installation/" },
              { label: "Routing", to: "/routing/overview" },
              { label: "Providers And Models", to: "/providers-models/overview" },
            ],
          },
          {
            title: "Operate",
            items: [
              { label: "Evaluation Guide", to: "/evaluation/overview" },
              { label: "Routing Decision Tree", to: "/routing/strategy-decision-tree" },
              { label: "Security And Trust", to: "/evaluation/security-and-trust" },
              { label: "Solution Brief", to: "/solution-brief" },
              { label: "Harbor Case Study", to: "/evaluation/harbor-case-study" },
              { label: "Upgrade Guide", to: "/release-notes/upgrade-guide" },
            ],
          },
          {
            title: "Legal",
            items: [
              { label: "Software Licenses", to: "/legal/software-licenses" },
              { label: "Privacy", to: "/privacy" },
              { label: "Source License", href: "https://github.com/metrum-ai/router/blob/main/LICENSE" },
              { label: "Trademarks", href: "https://github.com/metrum-ai/router/blob/main/TRADEMARKS.md" },
            ],
          },
        ],
        copyright: `Copyright © 2026 Metrum AI, Inc.`,
      },
      prism: {
        theme: lightCodeTheme,
        darkTheme: darkCodeTheme,
        additionalLanguages: ["bash", "go", "typescript", "yaml"],
      },
      mermaid: {
        theme: { light: "neutral", dark: "dark" },
        options: {
          themeVariables: {
            primaryColor: "#0b0b0d",
            primaryTextColor: "#ffffff",
            primaryBorderColor: "#cc28af",
            lineColor: "#fe005f",
            secondaryColor: "#16161a",
            tertiaryColor: "#465cda",
            fontFamily: "MetrumSans, Inter, system-ui, sans-serif",
          },
        },
      },
    }),
};

module.exports = config;
