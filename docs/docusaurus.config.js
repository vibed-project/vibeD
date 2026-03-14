// @ts-check

/** @type {import('@docusaurus/types').Config} */
const config = {
  title: 'vibeD',
  tagline: 'Workload Orchestrator for GenAI-generated Artifacts',
  url: 'https://vibed-project.github.io',
  baseUrl: '/vibeD/',
  organizationName: 'vibed-project',
  projectName: 'vibeD',
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
            href: 'https://github.com/vibed-project/vibeD',
            label: 'GitHub',
            position: 'right',
          },
        ],
      },
      footer: {
        style: 'dark',
        copyright: `Copyright ${new Date().getFullYear()} vibeD. Built with Docusaurus.`,
      },
      prism: {
        additionalLanguages: ['bash', 'yaml', 'go'],
      },
    }),
};

export default config;
