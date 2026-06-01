import { useState } from 'react'
import './SetupGuide.css'

function getMcpUrl(): string {
  const loc = window.location
  return `${loc.protocol}//${loc.host}/mcp/`
}

interface Props {
  /** When true, render the guide content always-expanded with no toggle —
   *  used by the dedicated /connect page. Defaults to false (collapsible
   *  inline widget). */
  alwaysOpen?: boolean
}

export default function SetupGuide({ alwaysOpen = false }: Props) {
  const [open, setOpen] = useState(alwaysOpen)
  const [copied, setCopied] = useState<string | null>(null)

  const mcpUrl = getMcpUrl()

  const httpConfig = JSON.stringify(
    {
      mcpServers: {
        vibed: {
          command: 'npx',
          args: ['mcp-remote', mcpUrl,'--allow-http'],
        },
      },
    },
    null,
    2,
  )

  const stdioConfig = JSON.stringify(
    {
      mcpServers: {
        vibed: {
          command: 'vibed',
          args: ['--config', '/path/to/vibed.yaml'],
        },
      },
    },
    null,
    2,
  )

  const copyToClipboard = async (text: string, id: string) => {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(id)
      setTimeout(() => setCopied(null), 2000)
    } catch {
      // Fallback for non-secure contexts
      const textarea = document.createElement('textarea')
      textarea.value = text
      document.body.appendChild(textarea)
      textarea.select()
      document.execCommand('copy')
      document.body.removeChild(textarea)
      setCopied(id)
      setTimeout(() => setCopied(null), 2000)
    }
  }

  return (
    <div className="setup-guide">
      {!alwaysOpen && (
        <button className="setup-toggle" onClick={() => setOpen(!open)}>
          <span className="setup-toggle-icon">{open ? '\u25BE' : '\u25B8'}</span>
          <span className="setup-toggle-title">Connect to Claude Desktop</span>
          {!open && <span className="setup-toggle-hint">Click to expand setup instructions</span>}
        </button>
      )}

      {open && (
        <div className="setup-content">
          <p className="setup-intro">
            Add vibeD as an MCP server in your Claude Desktop configuration to start deploying artifacts with AI.
          </p>

          {/* HTTP / Remote mode */}
          <div className="setup-option">
            <div className="setup-option-header">
              <span className="setup-option-badge setup-badge-recommended">recommended</span>
              <h4>HTTP mode</h4>
              <span className="setup-option-desc">Connect to this running vibeD instance</span>
            </div>
            <p className="setup-step">
              Add the following to your <code>claude_desktop_config.json</code>:
            </p>
            <div className="setup-code-block">
              <pre>{httpConfig}</pre>
              <button
                className="setup-copy-btn"
                onClick={() => copyToClipboard(httpConfig, 'http')}
              >
                {copied === 'http' ? 'Copied!' : 'Copy'}
              </button>
            </div>
            <p className="setup-file-hint">
              Config file location:
              <code>~/Library/Application Support/Claude/claude_desktop_config.json</code> (macOS)
              or <code>%APPDATA%\Claude\claude_desktop_config.json</code> (Windows)
            </p>
          </div>

          {/* Stdio / Local mode */}
          <div className="setup-option">
            <div className="setup-option-header">
              <h4>Stdio mode</h4>
              <span className="setup-option-desc">Run vibeD locally as a subprocess</span>
            </div>
            <p className="setup-step">
              If you prefer to run vibeD directly on your machine (no server needed):
            </p>
            <div className="setup-code-block">
              <pre>{stdioConfig}</pre>
              <button
                className="setup-copy-btn"
                onClick={() => copyToClipboard(stdioConfig, 'stdio')}
              >
                {copied === 'stdio' ? 'Copied!' : 'Copy'}
              </button>
            </div>
            <p className="setup-file-hint">
              Make sure <code>vibed</code> is in your <code>PATH</code> and update the config path to your <code>vibed.yaml</code>.
            </p>
          </div>

          <p className="setup-footer">
            After saving the config, restart Claude Desktop. vibeD tools like <code>deploy_artifact</code>, <code>list_artifacts</code>, and <code>rollback_artifact</code> will appear automatically.
          </p>
        </div>
      )}
    </div>
  )
}
