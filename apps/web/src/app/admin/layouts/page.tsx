// /admin/layouts — local administrative surface for M1 venue and layout work.
//
// The page is intentionally thin: it resolves the layout id posted by the form
// (or passed as a query parameter), reads that layout version through the typed
// administrative client, and hands the result to the editor view. All editor
// behavior lives in lib/layout-editor.ts and all writes go through the server
// actions in ./actions.ts.
//
// The route is server-rendered per request because it depends on local
// administrative configuration and live data; it is never prerendered at build
// time. Like the API itself, it is a local-development surface only.
import {
  ADMIN_TOKEN_ENV,
  createAdminClient,
  resolveAdminConfig,
} from "@/lib/admin-api";
import type { AdminProblem } from "@/lib/admin-api";
import { fromLayout } from "@/lib/layout-editor";
import { LayoutEditorView } from "./layout-editor-view";

export const dynamic = "force-dynamic";

interface LayoutsPageProps {
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}

export default async function LayoutsPage({ searchParams }: LayoutsPageProps) {
  const params = await searchParams;
  const posted = params.layoutId;
  const layoutId = (Array.isArray(posted) ? posted[0] : posted)?.trim() ?? "";

  return (
    <main id="main-content" className="foundation admin">
      <a className="skip-link" href="#main-content">
        Skip to content
      </a>

      <section className="card" aria-labelledby="open-layout">
        <p className="eyebrow">Local administration · M1 venue and layout</p>
        <h2 id="open-layout">Open a layout version</h2>
        <form className="fields" action="/admin/layouts" method="get">
          <label>
            Layout id
            <input type="text" name="layoutId" defaultValue={layoutId} />
          </label>
          <button type="submit">Open layout</button>
        </form>
      </section>

      {layoutId === "" ? (
        <section className="card">
          <p className="notice">
            Enter a layout version id to view and edit its rows. Rows may hold
            different seat counts.
          </p>
        </section>
      ) : (
        await renderLayout(layoutId)
      )}
    </main>
  );
}

async function renderLayout(layoutId: string) {
  const config = resolveAdminConfig(process.env);
  if (!config.ok) {
    return <ProblemCard problem={config.error} />;
  }

  const client = createAdminClient(config.value);
  const result = await client.getLayout(layoutId);
  if (!result.ok) {
    return <ProblemCard problem={result.error} />;
  }

  return (
    <section className="card">
      <LayoutEditorView initialLayout={fromLayout(result.value)} />
    </section>
  );
}

function ProblemCard({ problem }: { problem: AdminProblem }) {
  return (
    <section className="card problems" aria-labelledby="layout-problem">
      <h2 id="layout-problem">The layout could not be loaded</h2>
      <p>
        <code>{problem.code}</code> · HTTP {problem.status}
      </p>
      <p>{problem.detail}</p>
      <p className="notice">
        This surface works only in local development: the API must run with local
        administration enabled, and this page needs the same{" "}
        <code>{ADMIN_TOKEN_ENV}</code> token as the API.
      </p>
    </section>
  );
}
