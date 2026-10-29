"use client";

// Local administrative film and event forms.
//
// These components are presentation only: the form text is handed to the
// server actions in ./actions.ts, which validate it, call the API, and return a
// typed result. A problem result is rendered inline and never thrown, so a
// conflict such as scheduling against a draft layout version stays visible
// instead of breaking the page.
import { useRouter } from "next/navigation";
import { useState, useTransition } from "react";
import type { AdminProblem } from "@/lib/admin-api";
import { ADMIN_SCHEDULE_PATH } from "@/lib/scheduling";
import { createFilmAction, scheduleEventAction } from "./actions";

interface Feedback {
  kind: "success" | "error";
  message: string;
}

function problemMessage(problem: AdminProblem): string {
  return `${problem.code}: ${problem.detail}`;
}

export function FilmForm() {
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
        setFeedback({ kind: "error", message: problemMessage(result.error) });
        return;
      }
      setFeedback({
        kind: "success",
        message: `Created film ${result.value.id}: ${result.value.title} (${result.value.runtimeMinutes} min, ${result.value.rating}).`,
      });
      setTitle("");
      setRuntimeMinutes("");
      setRating("");
    });
  }

  return (
    <form className="fields" onSubmit={handleSubmit} aria-label="Create a film">
      <label>
        Title
        <input
          type="text"
          name="title"
          value={title}
          onChange={(event) => setTitle(event.target.value)}
          disabled={pending}
        />
      </label>
      <label>
        Runtime (minutes)
        <input
          type="number"
          name="runtimeMinutes"
          value={runtimeMinutes}
          onChange={(event) => setRuntimeMinutes(event.target.value)}
          disabled={pending}
        />
      </label>
      <label>
        Rating
        <input
          type="text"
          name="rating"
          value={rating}
          onChange={(event) => setRating(event.target.value)}
          disabled={pending}
        />
      </label>
      <button type="submit" disabled={pending}>
        Create film
      </button>
      {feedback ? <FeedbackLine feedback={feedback} /> : null}
    </form>
  );
}

export function EventForm() {
  const router = useRouter();
  const [roomId, setRoomId] = useState("");
  const [filmId, setFilmId] = useState("");
  const [layoutVersionId, setLayoutVersionId] = useState("");
  const [startsAt, setStartsAt] = useState("");
  const [feedback, setFeedback] = useState<Feedback | null>(null);
  const [pending, startTransition] = useTransition();

  function handleSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setFeedback(null);
    startTransition(async () => {
      const result = await scheduleEventAction({
        roomId,
        filmId,
        layoutVersionId,
        startsAt,
      });
      if (!result.ok) {
        setFeedback({ kind: "error", message: problemMessage(result.error) });
        return;
      }
      setFeedback({
        kind: "success",
        message: `Scheduled event ${result.value.id} at ${result.value.startsAt} (${result.value.status}). Loading its materialized seats.`,
      });
      router.push(
        `${ADMIN_SCHEDULE_PATH}?eventId=${encodeURIComponent(result.value.id)}`,
      );
      router.refresh();
    });
  }

  return (
    <form
      className="fields"
      onSubmit={handleSubmit}
      aria-label="Schedule an event"
    >
      <label>
        Room id
        <input
          type="text"
          name="roomId"
          value={roomId}
          onChange={(event) => setRoomId(event.target.value)}
          disabled={pending}
        />
      </label>
      <label>
        Film id
        <input
          type="text"
          name="filmId"
          value={filmId}
          onChange={(event) => setFilmId(event.target.value)}
          disabled={pending}
        />
      </label>
      <label>
        Published layout version id
        <input
          type="text"
          name="layoutVersionId"
          value={layoutVersionId}
          onChange={(event) => setLayoutVersionId(event.target.value)}
          disabled={pending}
        />
      </label>
      <label>
        Start time (UTC)
        <input
          type="datetime-local"
          name="startsAt"
          value={startsAt}
          onChange={(event) => setStartsAt(event.target.value)}
          disabled={pending}
        />
      </label>
      <button type="submit" disabled={pending}>
        Schedule event
      </button>
      {feedback ? <FeedbackLine feedback={feedback} /> : null}
    </form>
  );
}

function FeedbackLine({ feedback }: { feedback: Feedback }) {
  return (
    <p
      className={feedback.kind === "error" ? "feedback error" : "feedback"}
      role={feedback.kind === "error" ? "alert" : "status"}
    >
      {feedback.message}
    </p>
  );
}
