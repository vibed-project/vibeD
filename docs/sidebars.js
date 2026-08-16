/** @type {import('@docusaurus/plugin-content-docs').SidebarsConfig} */
const sidebars = {
  docs: [
    'intro',
    'migrating-to-v0.6',
    {
      type: 'category',
      label: 'Getting Started',
      items: ['getting-started/local-dev', 'getting-started/first-deployment', 'getting-started/installation'],
    },
    {
      type: 'category',
      label: 'Concepts',
      items: ['concepts/architecture', 'concepts/control-plane', 'concepts/deploy-pipeline', 'concepts/lanes-and-templates', 'concepts/sandbox-isolation', 'concepts/app-lifecycle'],
    },
    {
      type: 'category',
      label: 'Configuration',
      items: ['configuration/config-reference', 'configuration/authentication', 'configuration/storage', 'configuration/custom-base-images', 'configuration/egress-control', 'configuration/quotas', 'configuration/audit-log', 'configuration/registry'],
    },
    {
      type: 'category',
      label: 'Extending vibeD',
      items: ['extending/overview', 'extending/store-backends', 'extending/auth-providers', 'extending/tenancy', 'extending/policy-and-metering', 'extending/secrets-and-features'],
    },
    {
      type: 'category',
      label: 'Deployment',
      items: [
        'deployment/production-guide',
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
        'mcp-tools/list-versions',
        'mcp-tools/rollback-artifact',
        'mcp-tools/create-share-link',
        'mcp-tools/list-share-links',
        'mcp-tools/revoke-share-link',
        'mcp-tools/user-management',
      ],
    },
    {
      type: 'category',
      label: 'Reference',
      items: ['reference/http-api'],
    },
    {
      type: 'category',
      label: 'Development',
      items: ['development/testing', 'development/manual-testing'],
    },
  ],
};

export default sidebars;
