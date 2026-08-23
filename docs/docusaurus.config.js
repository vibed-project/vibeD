// @ts-check

/** @type {import('@docusaurus/types').Config} */
const config = {
  title: 'vibeD',
  tagline: 'Workload Orchestrator for GenAI-generated Artifacts',
  url: 'https://vibed.run',
  baseUrl: '/',
  organizationName: 'vibed-project',
  projectName: 'vibed-project.github.io',
  favicon: 'img/vibed.ico',
  onBrokenLinks: 'throw',

  presets: [
    [
      'classic',
      /** @type {import('@docusaurus/preset-classic').Options} */
      ({
        docs: {
          sidebarPath: './sidebars.js',
          editUrl: 'https://github.com/vibed-project/vibeD/tree/main/docs/',
        },
        blog: {
          showReadingTime: true,
          editUrl: 'https://github.com/vibed-project/vibeD/tree/main/docs/',
          blogTitle: 'Blog & Release Notes',
          blogDescription: 'vibeD project updates, release notes, and technical deep-dives.',
          blogSidebarTitle: 'Recent posts',
          blogSidebarCount: 'ALL',
        },
        theme: {
          customCss: './src/css/custom.css',
        },
      }),
    ],
  ],

  themeConfig:
    /** @type {import('@docusaurus/preset-classic').ThemeConfig} */
    ({
      navbar: {
        title: 'vibeD',
        logo: {
          alt: 'vibeD Logo',
          src: 'img/vibed-logo.webp',
        },
        items: [
          {
            type: 'docSidebar',
            sidebarId: 'docs',
            position: 'left',
            label: 'Docs',
          },
          {
            to: '/blog',
            label: 'Blog',
            position: 'left',
          },
          {
            // Current release, shown left of the GitHub link. Bump on release.
            href: 'https://github.com/vibed-project/vibeD/releases',
            label: 'v0.6.0',
            position: 'right',
          },
          {
            href: 'https://github.com/vibed-project/vibeD',
            label: 'GitHub',
            position: 'right',
          },
        ],
      },
      footer: {
        style: 'dark',
        links: [
          {
            title: 'Docs',
            items: [
              {label: 'Overview', to: '/docs/'},
              {label: 'Installation', to: '/docs/getting-started/installation'},
              {label: 'First deployment', to: '/docs/getting-started/first-deployment'},
              {label: 'HTTP API', to: '/docs/reference/http-api'},
            ],
          },
          {
            title: 'More',
            items: [
              {label: 'Blog', to: '/blog'},
              {label: 'Releases', href: 'https://github.com/vibed-project/vibeD/releases'},
              {label: 'GitHub', href: 'https://github.com/vibed-project/vibeD'},
            ],
          },
          {
            // The sibling projects. Each of their sites links back here, so the
            // four are reachable from any one of them.
            title: 'The stack',
            items: [
              {label: 'hiveD (control plane)', href: 'https://vibed-project.github.io/hiveD/'},
              {label: 'mindD (memory)', href: 'https://vibed-project.github.io/mindD/'},
              {label: 'routeD (model routing)', href: 'https://vibed-project.github.io/routeD/'},
            ],
          },
        ],
        copyright: `Copyright ${new Date().getFullYear()} vibeD. Built with Docusaurus.`,
      },
      prism: {
        additionalLanguages: ['bash', 'yaml', 'go'],
      },
    }),
};

export default config;
