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
        eyebrow="Administración local · M3.5"
        title="Editor de planos"
        description={
          <>
            Filas y butacas de una versión del plano. Un borrador se puede editar y publicar,
            y una versión publicada es inmutable — clónala para cambiarla. Sólo funciona en
            desarrollo local: la API tiene que correr con la administración local activada, y
            esta página necesita el mismo <code>{ADMIN_TOKEN_ENV}</code> que la API.
          </>
        }
      />

      <Card>
        <CardHeader>
          <h2 className="m-0 text-lg leading-tight font-semibold">
            Abrir una versión del plano
          </h2>
          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            Todas las versiones de una sala están en{" "}
            <Link href="/admin/venues">cines, salas y planos</Link>, donde su propio enlace la
            abre aquí. También se puede escribir un identificador directamente.
          </p>
        </CardHeader>
        <CardContent>
          <form
            className="flex flex-wrap items-end gap-4"
            action="/admin/layouts"
            method="get"
            aria-label="Abrir una versión del plano"
          >
            <div className="flex min-w-72 flex-col gap-1.5">
              <Label htmlFor="layout-id">Identificador de la versión del plano</Label>
              <Input id="layout-id" name="layoutId" defaultValue={layoutId} />
            </div>
            <Button type="submit" variant="outline">
              Abrir el plano
            </Button>
          </form>
        </CardContent>
      </Card>

      {layoutId === "" ? (
        <Card>
          <CardContent className="pt-5">
            <p className="m-0 max-w-prose text-sm text-muted-foreground">
              Escribe el identificador de una versión del plano para ver y editar sus filas.
              Las filas pueden tener distinto número de butacas, así que una fila puede tener
              una sola butaca y la siguiente doce.
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
 * API's own detail are printed, and nothing is offered for editing. The API's detail stays in
 * the API's own words: it is the box office's sentence, not this screen's.
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
            No se pudo cargar el plano
          </h2>
          <AlertDescription className="flex flex-col gap-1">
            <p className="m-0">
              <code>{problem.code}</code> · HTTP {problem.status}
            </p>
            <p className="m-0">{problem.detail}</p>
            <p className="m-0">
              Esta pantalla sólo funciona en desarrollo local: la API tiene que correr con la
              administración local activada, y esta página necesita el mismo{" "}
              <code>{ADMIN_TOKEN_ENV}</code> que la API.
            </p>
          </AlertDescription>
        </Alert>
      </CardContent>
    </Card>
  );
}
