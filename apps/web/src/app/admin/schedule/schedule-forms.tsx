"use client";

// Local administrative film, screening and sale-state forms (M3.5 · T8 · S3, S4, S8).
//
// These components are presentation only. Every rule they apply is a pure function in
// lib/scheduling.ts — which statuses are offered, how a typed price becomes minor units,
// which fields one PATCH carries, and which input a `400` named — and every write goes
// through the server actions in ./actions.ts. A problem result is rendered inline and
// never thrown, so a conflict such as scheduling against a draft layout version stays
// visible instead of breaking the page.
//
// Three behaviours are worth naming here:
//
//   - **The API's words, not ours.** A refusal prints `problem.detail` exactly as the API
//     sent it, under its code and HTTP status. Nothing is reworded and nothing is
//     summarised, because every `400` already names its field and inventing friendlier copy
//     is how a screen ends up describing a different problem (S8).
//   - **One submit can put a screening on sale, and the screen says which one it did.**
//     `POST /admin/events` carries an optional price and an optional status (M4.1 · T3b),
//     so the create form schedules a screening that is already priced and on sale when the
//     operator filled those two controls in. Left empty — the price blank, the status on
//     its default draft — the same form sends a request that names neither, and the
//     sale-state form below it is still how a screening is priced afterwards.
//   - **A price that did not change is not sent.** The sale-state form starts from the price
//     the API served and sends only the fields the operator changed, which is the API's own
//     per-field rule applied on this side of the wire.
import { useRouter } from "next/navigation";
import { useId, useState, useTransition } from "react";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import type { AdminProblem } from "@/lib/admin-api";
import type { EventStatus, ScreeningWritable } from "@/lib/scheduling";
import {
  ADMIN_SCHEDULE_PATH,
  DEFAULT_EVENT_STATUS,
  EVENT_STATUS_OPTIONS,
  eventStatusOption,
  fieldProblem,
  toneBadgeVariant,
} from "@/lib/scheduling";
import { createFilmAction, scheduleEventAction, updateScreeningAction } from "./actions";

/** One option of a scheduling select, reduced to what the control needs. */
export interface ScheduleOption {
  id: string;
  /** The single line the option shows. */
  label: string;
}

interface Feedback {
  kind: "success" | "error";
  message: string;
}

/** The API's own refusal, printed unchanged: the code, the HTTP status and its detail. */
function ProblemMessage({ problem }: { problem: AdminProblem }) {
  return (
    <AlertDescription className="flex flex-col gap-1">
      <p className="m-0">
        <code>{problem.code}</code> · HTTP {problem.status}
      </p>
      <p className="m-0">{problem.detail}</p>
    </AlertDescription>
  );
}

/** A confirmation or a refusal, announced politely or interruptively as it deserves. */
function FeedbackLine({ feedback }: { feedback: Feedback }) {
  const error = feedback.kind === "error";
  return (
    <Alert variant={error ? "destructive" : "success"} role={error ? "alert" : "status"}>
      <AlertDescription className="m-0">{feedback.message}</AlertDescription>
    </Alert>
  );
}

/**
 * Creates one film.
 *
 * The words are Castilian since the Marquesina redesign; the rules this form applies did not
 * move with them. The confirmation names what the API stored, so it says what can be done
 * next instead of what the form hoped for.
 */
export function FilmForm() {
  const router = useRouter();
  const ids = useIds("film");
  const [title, setTitle] = useState("");
  const [runtimeMinutes, setRuntimeMinutes] = useState("");
  const [rating, setRating] = useState("");
  const [feedback, setFeedback] = useState<Feedback | null>(null);
  const [pending, startTransition] = useTransition();

  function handleSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setFeedback(null);
    startTransition(async () => {
      const result = await createFilmAction({ title, runtimeMinutes, rating });
      if (!result.ok) {
        setFeedback({
          kind: "error",
          message: `${result.error.code}: ${result.error.detail}`,
        });
        return;
      }
      setFeedback({
        kind: "success",
        message: `Creada ${result.value.title} (${result.value.runtimeMinutes} min, ${result.value.rating}). Ya se puede programar.`,
      });
      setTitle("");
      setRuntimeMinutes("");
      setRating("");
      // The repertoire the screening form offers is read on the server, so the page is
      // re-rendered rather than patched: a film the form did not know about would be a
      // film nobody can pick.
      router.refresh();
    });
  }

  return (
    <form className="flex flex-col gap-4" onSubmit={handleSubmit} aria-label="Crear una película">
      <div className="flex flex-wrap items-end gap-4">
        <div className="flex min-w-56 flex-col gap-1.5">
          <Label htmlFor={ids.title}>Título</Label>
          <Input
            id={ids.title}
            name="title"
            value={title}
            onChange={(event) => setTitle(event.target.value)}
            disabled={pending}
          />
        </div>
        <div className="flex w-40 flex-col gap-1.5">
          <Label htmlFor={ids.runtime}>Duración (minutos)</Label>
          <Input
            id={ids.runtime}
            name="runtimeMinutes"
            type="number"
            inputMode="numeric"
            value={runtimeMinutes}
            onChange={(event) => setRuntimeMinutes(event.target.value)}
            disabled={pending}
          />
        </div>
        <div className="flex w-28 flex-col gap-1.5">
          <Label htmlFor={ids.rating}>Calificación</Label>
          <Input
            id={ids.rating}
            name="rating"
            value={rating}
            onChange={(event) => setRating(event.target.value)}
            disabled={pending}
          />
        </div>
        <Button type="submit" disabled={pending}>
          {pending ? "Creando…" : "Crear película"}
        </Button>
      </div>
      {feedback ? <FeedbackLine feedback={feedback} /> : null}
    </form>
  );
}

/**
 * Schedules one screening, optionally already priced and on sale.
 *
 * The film comes from the repertoire the API lists and the layout version from that room's
 * published versions, so this form never asks an operator to paste an identifier: the two
 * values the write needs are two values the reads already named. A room with no published
 * layout version says so, because a draft version is the API's conflict and a form that
 * offered one would be inviting a refusal.
 *
 * The price and the status are the two optional fields `POST /admin/events` grew (M4.1 ·
 * T3b · S9): filling them in puts a screening on sale in this one submit, while leaving the
 * price empty and the status on its default draft sends the request this form always sent,
 * a draft at no price.
 */
export function EventForm({
  films,
  layouts,
  cinemaId,
  roomId,
}: {
  films: readonly ScheduleOption[];
  layouts: readonly ScheduleOption[];
  cinemaId: string;
  roomId: string;
}) {
  const router = useRouter();
  const ids = useIds("screening");
  const [filmId, setFilmId] = useState("");
  const [layoutVersionId, setLayoutVersionId] = useState("");
  const [startsAt, setStartsAt] = useState("");
  const [price, setPrice] = useState("");
  const [status, setStatus] = useState<EventStatus>(DEFAULT_EVENT_STATUS);
  const [feedback, setFeedback] = useState<Feedback | null>(null);
  const [pending, startTransition] = useTransition();

  const ready = roomId !== "" && films.length > 0 && layouts.length > 0;
  const chosenStatus = eventStatusOption(status);

  function handleSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setFeedback(null);
    startTransition(async () => {
      const result = await scheduleEventAction({
        roomId,
        filmId,
        layoutVersionId,
        startsAt,
        price,
        status,
      });
      if (!result.ok) {
        setFeedback({
          kind: "error",
          message: `${result.error.code}: ${result.error.detail}`,
        });
        return;
      }
      // The confirmation says what the API stored, not what the form hoped for: the status
      // and the price it answers with are the ones the row now holds.
      const created = result.value;
      const createdStatus = eventStatusOption(created.status);
      setFeedback({
        kind: "success",
        message:
          created.status === "on_sale"
            ? `Creada la sesión a las ${created.startsAt} con precio ${created.priceDisplay}, en venta: la cartelera la lista mientras su inicio siga por delante.`
            : `Creada la sesión a las ${created.startsAt} con precio ${created.priceDisplay}, en estado ${(createdStatus?.label ?? created.status).toLowerCase()}. Su precio y su estado se pueden cambiar abajo.`,
      });
      setStartsAt("");
      setPrice("");
      setStatus(DEFAULT_EVENT_STATUS);
      // The sale-state form reads its values from the list, so opening the new screening
      // is what fills it in — with the price and status the create just stored.
      const query = new URLSearchParams({ cinemaId, roomId, eventId: result.value.id });
      router.push(`${ADMIN_SCHEDULE_PATH}?${query.toString()}`);
      router.refresh();
    });
  }

  return (
    <form className="flex flex-col gap-4" onSubmit={handleSubmit} aria-label="Programar una sesión">
      <div className="flex flex-wrap items-end gap-4">
        <div className="flex min-w-56 flex-col gap-1.5">
          <Label htmlFor={ids.film}>Película</Label>
          <Select value={filmId} onValueChange={setFilmId} disabled={pending || films.length === 0}>
            <SelectTrigger id={ids.film} aria-describedby={ids.filmHint}>
              <SelectValue placeholder="Elige una película" />
            </SelectTrigger>
            <SelectContent>
              {films.map((film) => (
                <SelectItem key={film.id} value={film.id}>
                  {film.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <p id={ids.filmHint} className="m-0 text-sm text-muted-foreground">
            {films.length === 0
              ? "El repertorio está vacío. Crea una película primero: este formulario no puede programar una que no exista."
              : "Se elige de la lista que sirve la API, nunca se escribe un identificador."}
          </p>
        </div>

        <div className="flex min-w-56 flex-col gap-1.5">
          <Label htmlFor={ids.layout}>Versión publicada del plano</Label>
          <Select
            value={layoutVersionId}
            onValueChange={setLayoutVersionId}
            disabled={pending || layouts.length === 0}
          >
            <SelectTrigger id={ids.layout} aria-describedby={ids.layoutHint}>
              <SelectValue placeholder="Elige una versión del plano" />
            </SelectTrigger>
            <SelectContent>
              {layouts.map((layout) => (
                <SelectItem key={layout.id} value={layout.id}>
                  {layout.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <p id={ids.layoutHint} className="m-0 max-w-prose text-sm text-muted-foreground">
            {layouts.length === 0
              ? "Esta sala no tiene ninguna versión publicada del plano, así que una sesión en ella se rechazaría como conflicto. Publica una en el editor de planos primero."
              : "Sólo se ofrecen las versiones publicadas: una versión en borrador se rechaza como conflicto."}
          </p>
        </div>

        <div className="flex min-w-56 flex-col gap-1.5">
          <Label htmlFor={ids.startsAt}>Hora de inicio (UTC)</Label>
          <Input
            id={ids.startsAt}
            name="startsAt"
            type="datetime-local"
            value={startsAt}
            onChange={(event) => setStartsAt(event.target.value)}
            disabled={pending}
          />
        </div>

        <div className="flex min-w-44 flex-col gap-1.5">
          <Label htmlFor={ids.price}>Precio por butaca</Label>
          <Input
            id={ids.price}
            name="price"
            inputMode="decimal"
            value={price}
            onChange={(event) => setPrice(event.target.value)}
            disabled={pending}
            aria-describedby={ids.priceHint}
          />
          <p id={ids.priceHint} className="m-0 max-w-prose text-sm text-muted-foreground">
            Como lo escribe una persona: <code>12.50</code>. Si se deja vacío, este
            formulario no nombra ningún precio y la sesión se guarda sin precio, para
            ponerle uno abajo.
          </p>
        </div>

        <div className="flex min-w-56 flex-col gap-1.5">
          <Label htmlFor={ids.status}>Estado</Label>
          <Select
            value={status}
            onValueChange={(value) => setStatus(value as EventStatus)}
            disabled={pending}
          >
            <SelectTrigger id={ids.status} aria-describedby={ids.statusHint}>
              <SelectValue placeholder="Elige un estado" />
            </SelectTrigger>
            <SelectContent>
              {EVENT_STATUS_OPTIONS.map((option) => (
                <SelectItem key={option.value} value={option.value}>
                  {option.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <p id={ids.statusHint} className="m-0 max-w-prose text-sm text-muted-foreground">
            {chosenStatus?.note ?? ""}
          </p>
        </div>

        <Button type="submit" disabled={pending || !ready}>
          {pending ? "Programando…" : "Programar la sesión"}
        </Button>
      </div>

      <p className="m-0 max-w-prose text-sm text-muted-foreground">
        Un solo envío: un precio y un estado <code>on_sale</code> aquí ponen la sesión en la
        cartelera. Si dejas el precio vacío y el estado en borrador, se programa como un
        borrador sin precio, igual que antes, y fijas los dos en el formulario de abajo cuando
        quieras.
      </p>

      {feedback ? <FeedbackLine feedback={feedback} /> : null}
    </form>
  );
}

/**
 * Sets one screening's price, its status, or both — for a screening that already exists.
 *
 * The price field opens on the amount the API served, so "unchanged" is a real possibility:
 * a field the operator did not touch is not sent, and a status-only change therefore leaves
 * the price exactly where it was. Submitting without changing anything is refused locally in
 * the API's own words rather than sent as a request the server would answer with the same
 * sentence.
 */
export function SaleStateForm({
  screening,
  heading,
}: {
  screening: ScreeningWritable;
  heading: string;
}) {
  const router = useRouter();
  const ids = useIds("sale-state");
  const [price, setPrice] = useState(screening.priceDisplay);
  const [status, setStatus] = useState(screening.status);
  const [feedback, setFeedback] = useState<Feedback | null>(null);
  const [refused, setRefused] = useState<AdminProblem | null>(null);
  const [pending, startTransition] = useTransition();

  const chosen = eventStatusOption(status);

  function handleSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setFeedback(null);
    setRefused(null);
    startTransition(async () => {
      const result = await updateScreeningAction(screening, { price, status });
      if (!result.ok) {
        setRefused(result.error);
        return;
      }
      setFeedback({
        kind: "success",
        message: `Guardado. La sesión tiene ahora ${result.value.priceDisplay} por butaca y su estado guardado es ${result.value.status}.`,
      });
      router.refresh();
    });
  }

  // The API names the input it refused at the start of `detail`, so the refusal points at
  // the control it is about instead of at the whole form.
  const refusedField = refused === null ? "" : fieldProblem(refused).field;
  const priceInvalid = refusedField === "priceCents";
  const statusInvalid = refusedField === "status";

  return (
    <form
      className="flex flex-col gap-4"
      onSubmit={handleSubmit}
      aria-label={`Precio y estado de ${heading}`}
    >
      <div className="flex flex-wrap items-start gap-4">
        <div className="flex min-w-44 flex-col gap-1.5">
          <Label htmlFor={ids.price}>Precio por butaca</Label>
          <Input
            id={ids.price}
            name="price"
            inputMode="decimal"
            value={price}
            onChange={(event) => setPrice(event.target.value)}
            disabled={pending}
            aria-invalid={priceInvalid}
            aria-describedby={`${ids.priceHint}${priceInvalid ? ` ${ids.error}` : ""}`}
          />
          <p id={ids.priceHint} className="m-0 max-w-prose text-sm text-muted-foreground">
            Como lo escribe una persona: <code>12.50</code>. Cero es una sesión gratuita,
            no un precio que falta.
          </p>
        </div>

        <div className="flex min-w-56 flex-col gap-1.5">
          <Label htmlFor={ids.status}>Estado</Label>
          <Select value={status} onValueChange={setStatus} disabled={pending}>
            <SelectTrigger
              id={ids.status}
              aria-invalid={statusInvalid}
              aria-describedby={`${ids.statusHint}${statusInvalid ? ` ${ids.error}` : ""}`}
            >
              <SelectValue placeholder="Elige un estado" />
            </SelectTrigger>
            <SelectContent>
              {EVENT_STATUS_OPTIONS.map((option) => (
                <SelectItem key={option.value} value={option.value}>
                  {option.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <p id={ids.statusHint} className="m-0 max-w-prose text-sm text-muted-foreground">
            {chosen === undefined
              ? "El estado guardado de esta sesión no es uno de los que ofrece esta versión. Elegir uno arriba lo sustituye."
              : chosen.note}
          </p>
        </div>

        <Button type="submit" disabled={pending || price.trim() === "" || status.trim() === ""}>
          {pending ? "Guardando…" : "Guardar precio y estado"}
        </Button>
      </div>

      {chosen === undefined ? null : (
        <p className="m-0 flex flex-wrap items-center gap-2 text-sm">
          <span className="text-muted-foreground">Este estado se lee</span>
          <Badge variant={toneBadgeVariant(chosen.tone)}>{chosen.label}</Badge>
        </p>
      )}

      {refused === null ? null : (
        <Alert id={ids.error} variant="destructive" role="alert">
          <ProblemMessage problem={refused} />
        </Alert>
      )}

      {feedback ? <FeedbackLine feedback={feedback} /> : null}
    </form>
  );
}

/** A stable id set for the controls of one form and the text that describes them. */
function useIds(prefix: string) {
  const base = useId();
  return {
    title: `${base}-${prefix}-title`,
    runtime: `${base}-${prefix}-runtime`,
    rating: `${base}-${prefix}-rating`,
    film: `${base}-${prefix}-film`,
    filmHint: `${base}-${prefix}-film-hint`,
    layout: `${base}-${prefix}-layout`,
    layoutHint: `${base}-${prefix}-layout-hint`,
    startsAt: `${base}-${prefix}-starts-at`,
    price: `${base}-${prefix}-price`,
    priceHint: `${base}-${prefix}-price-hint`,
    status: `${base}-${prefix}-status`,
    statusHint: `${base}-${prefix}-status-hint`,
    error: `${base}-${prefix}-error`,
  };
}
