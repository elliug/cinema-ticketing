"use server";

// Server actions for the local administrative layout editor.
//
// Each action is a thin adapter: it resolves the local administrative client
// configuration from server-side environment variables, delegates to the typed
// API client, and returns the typed result unchanged. All behavior lives in
// lib/admin-api.ts (HTTP) and lib/layout-editor.ts (model), both of which are
// covered by tests.
import { revalidatePath } from "next/cache";
import type {
  AdminClient,
  AdminLayout,
  AdminResult,
  RowPayload,
} from "@/lib/admin-api";
import { createAdminClient, resolveAdminConfig } from "@/lib/admin-api";

const ADMIN_LAYOUTS_PATH = "/admin/layouts";

async function withClient<T>(
  call: (client: AdminClient) => Promise<AdminResult<T>>,
): Promise<AdminResult<T>> {
  const config = resolveAdminConfig(process.env);
  if (!config.ok) {
    return { ok: false, error: config.error };
  }
  return call(createAdminClient(config.value));
}

/** Replaces the rows of one draft layout version. */
export async function saveRowsAction(
  layoutId: string,
  rows: readonly RowPayload[],
): Promise<AdminResult<AdminLayout>> {
  const result = await withClient((client) => client.setRows(layoutId, rows));
  if (result.ok) {
    revalidatePath(ADMIN_LAYOUTS_PATH);
  }
  return result;
}

/** Publishes one draft layout version, which freezes it. */
export async function publishLayoutAction(
  layoutId: string,
): Promise<AdminResult<AdminLayout>> {
  const result = await withClient((client) => client.publishLayout(layoutId));
  if (result.ok) {
    revalidatePath(ADMIN_LAYOUTS_PATH);
  }
  return result;
}

/** Copies a published layout version into a new editable draft. */
export async function cloneLayoutAction(
  layoutId: string,
): Promise<AdminResult<AdminLayout>> {
  const result = await withClient((client) => client.cloneLayout(layoutId));
  if (result.ok) {
    revalidatePath(ADMIN_LAYOUTS_PATH);
  }
  return result;
}
