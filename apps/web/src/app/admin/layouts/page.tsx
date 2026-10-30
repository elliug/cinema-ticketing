// /admin/layouts — local administrative surface for venue and layout work (M3.5 · T8 · S3).
//
// The page is intentionally thin: it resolves the layout id posted by the form (or passed
// as a query parameter), reads that layout version through the typed administrative client,
// and hands the result to the editor view. All editor behavior lives in
// lib/layout-editor.ts and all writes go through the server actions in ./actions.ts.
//
// The one `h1` is the page's, from `PageHeader`; the editor below it names the version it is
// editing in an `h2`, so the hierarchy is the page's business and not the editor's.
//
// The route is server-rendered per request because it depends on local administrative
// configuration and live data; it is never prerendered at build time. Like the API itself,
// it is a local-development surface only.
import Link from "next/link";

import { PageHeader } from "@/components";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
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
    <main id="main-content" className="foundation">
      <PageHeader
        eyebrow="Local administration · M3.5"
        title="Layout editor"
        description={
          <>
            Rows and seats of one layout version. A draft can be edited and published, and a
            published version is immutable — clone it to change it. Works in local
            development only: the API must run with local administration enabled, and this
            page needs the same <code>{ADMIN_TOKEN_ENV}</code> token as the API.
          </>
        }
      />

      <Card>
        <CardHeader>
          <h2 className="m-0 text-lg leading-tight font-semibold">Open a layout version</h2>
          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            Every version of a room is listed in{" "}
            <Link href="/admin/venues">cinemas, rooms and layouts</Link>, where its own link opens it
            here. An identifier can also be typed directly.
          </p>
        </CardHeader>
        <CardContent>
          <form
            className="flex flex-wrap items-end gap-4"
            action="/admin/layouts"
            method="get"
            aria-label="Open a layout version"
          >
            <div className="flex min-w-72 flex-col gap-1.5">
              <Label htmlFor="layout-id">Layout version id</Label>
              <Input id="layout-id" name="layoutId" defaultValue={layoutId} />
            </div>
            <Button type="submit" variant="outline">
              Open layout
            </Button>
          </form>
        </CardContent>
      </Card>

      {layoutId === "" ? (
        <Card>
          <CardContent className="pt-5">
            <p className="m-0 max-w-prose text-sm text-muted-foreground">
              Enter a layout version id to view and edit its rows. Rows may hold different
              seat counts, so one row can have a single seat while the next has twelve.
            </p>
          </CardContent>
        </Card>
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

  return <LayoutEditorView initialLayout={fromLayout(result.value)} />;
}

/**
 * The layout could not be read, printed as the API answered it.
 *
 * A version this page cannot read is not an empty draft: the code, the HTTP status and the
 * API's own detail are printed, and nothing is offered for editing.
 */
function ProblemCard({ problem }: { problem: AdminProblem }) {
  return (
    <Card>
      <CardContent className="pt-5">
        <Alert
          variant="destructive"
          role="alert"
          aria-labelledby="layout-problem"
          className="flex flex-col gap-2"
        >
          <h2 id="layout-problem" className="m-0 text-lg leading-tight font-semibold">
            The layout could not be loaded
          </h2>
          <AlertDescription className="flex flex-col gap-1">
            <p className="m-0">
              <code>{problem.code}</code> · HTTP {problem.status}
            </p>
            <p className="m-0">{problem.detail}</p>
            <p className="m-0">
              This surface works only in local development: the API must run with local
              administration enabled, and this page needs the same{" "}
              <code>{ADMIN_TOKEN_ENV}</code> token as the API.
            </p>
          </AlertDescription>
        </Alert>
      </CardContent>
    </Card>
  );
}
