import SetupGuide from '../components/SetupGuide'

interface Props {
  onBack: () => void
}

/**
 * ConnectPage is the dedicated home for "Connect to Claude Desktop" — the
 * MCP setup guide that previously lived as a collapsible widget on the
 * dashboard. SetupGuide's alwaysOpen prop renders the same content without
 * its toggle button so the page title carries the framing instead.
 */
export default function ConnectPage({ onBack }: Props) {
  return (
    <main className="main">
      <div className="page-header">
        <button className="back-btn" onClick={onBack} aria-label="Back to dashboard">← Back</button>
        <h1 className="page-title">How to connect</h1>
      </div>

      <section className="section">
        <SetupGuide alwaysOpen />
      </section>
    </main>
  )
}
