/** @type {import('@docusaurus/plugin-content-docs').SidebarsConfig} */
const sidebars = {
  docs: [
    'intro',
    {
      type: 'category',
      label: 'Getting Started',
      items: ['getting-started/installation', 'getting-started/local-dev', 'getting-started/first-deployment'],
    },
    {
      type: 'category',
      label: 'Concepts',
      items: ['concepts/architecture', 'concepts/deployment-targets', 'concepts/artifact-lifecycle'],
    },
    {
      type: 'category',
      label: 'Configuration',
      items: ['configuration/config-reference', 'configuration/authentication', 'configuration/storage', 'configuration/registry'],
    },
    {
      type: 'category',
      label: 'Deployment',
      items: [
        'deployment/production-guide',
        'deployment/knative-setup',
        'deployment/monitoring',
        'deployment/troubleshooting',
      ],
    },
    {
      type: 'category',
      label: 'MCP Tools',
      items: [
        'mcp-tools/overview',
        'mcp-tools/deploy-artifact',
        'mcp-tools/list-artifacts',
        'mcp-tools/get-artifact-status',
        'mcp-tools/update-artifact',
        'mcp-tools/delete-artifact',
        'mcp-tools/get-artifact-logs',
        'mcp-tools/list-deployment-targets',
        'mcp-tools/list-versions',
        'mcp-tools/rollback-artifact',
        'mcp-tools/share-artifact',
        'mcp-tools/unshare-artifact',
      ],
    },
  ],
};

export default sidebars;
