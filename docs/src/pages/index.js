import React from 'react';
import Layout from '@theme/Layout';
import Link from '@docusaurus/Link';
import useDocusaurusContext from '@docusaurus/useDocusaurusContext';
import useBaseUrl from '@docusaurus/useBaseUrl';

function HomepageHeader() {
  const {siteConfig} = useDocusaurusContext();
  return (
    <header style={{
      padding: '4rem 0',
      textAlign: 'center',
      position: 'relative',
      overflow: 'hidden',
    }}>
      <div className="container">
        <img
          src={useBaseUrl('/img/vibed-logo.webp')}
          alt="vibeD Logo"
          style={{width: 160, height: 160, marginBottom: '1rem'}}
        />
        <h1 style={{fontSize: '3rem'}}>{siteConfig.title}</h1>
        <p style={{fontSize: '1.5rem', opacity: 0.8}}>{siteConfig.tagline}</p>
        <div style={{display: 'flex', gap: '1rem', justifyContent: 'center', marginTop: '2rem'}}>
          <Link
            className="button button--primary button--lg"
            to="/docs">
            Get Started
          </Link>
          <Link
            className="button button--secondary button--lg"
            to="/docs/getting-started/installation">
            Installation
          </Link>
        </div>
        <video
          controls
          preload="metadata"
          style={{
            display: 'block',
            margin: '2.5rem auto 0',
            width: '100%',
            maxWidth: 880,
            borderRadius: 12,
            boxShadow: '0 8px 30px rgba(0, 0, 0, 0.2)',
          }}>
          <source src={useBaseUrl('/video/vibeD-0.3.1-example.mp4')} type="video/mp4" />
          Your browser can't play this video.{' '}
          <a href={useBaseUrl('/video/vibeD-0.3.1-example.mp4')}>Download the demo</a>.
        </video>
      </div>
    </header>
  );
}

function Feature({title, description}) {
  return (
    <div style={{flex: 1, padding: '1rem'}}>
      <h3>{title}</h3>
      <p>{description}</p>
    </div>
  );
}

export default function Home() {
  return (
    <Layout
      title="Home"
      description="Workload Orchestrator for GenAI-generated Artifacts">
      <HomepageHeader />
      <main>
        <section style={{padding: '2rem 0'}}>
          <div className="container">
            <div style={{display: 'flex', gap: '2rem', flexWrap: 'wrap'}}>
              <Feature
                title="MCP Native"
                description="Expose deployment tools via the Model Context Protocol. Any AI coding tool (Claude, Gemini, ChatGPT) can deploy directly to your infrastructure."
              />
              <Feature
                title="Two Lanes, Auto-Selected"
                description="A deterministic classifier routes each upload to the fast lane (workerd V8 isolates / static nginx) or the general lane (Kata + Firecracker microVM) for hardware-grade isolation."
              />
              <Feature
                title="No Build on the Deploy Path"
                description="Source is injected into a pre-booted, warm sandbox and started in seconds. Template images are built by CI on template changes, never per deploy."
              />
            </div>
          </div>
        </section>
      </main>
    </Layout>
  );
}
