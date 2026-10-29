"use client";

// Local administrative venue forms.
//
// These components are presentation only: the form text is handed to the
// server actions in ./actions.ts, which validate it, call the API, and return a
// typed result. A problem result is rendered inline and never thrown, so a
// rejected write such as a duplicate room name stays visible instead of
// breaking the page.
import { useRouter } from "next/navigation";
import { useState, useTransition } from "react";
import type { AdminProblem } from "@/lib/admin-api";
import { ADMIN_VENUES_PATH } from "@/lib/venue-admin";
import {
  createCinemaAction,
  createLayoutAction,
  createRoomAction,
} from "./actions";

interface Feedback {
  kind: "success" | "error";
  message: string;
}

/** One selectable cinema or room, reduced to what a select needs. */
export interface VenueOption {
  id: string;
  label: string;
}

function problemMessage(problem: AdminProblem): string {
  return `${problem.code}: ${problem.detail}`;
}

/** Creates one cinema. */
export function CinemaForm() {
  const router = useRouter();
  const [name, setName] = useState("");
  const [timezone, setTimezone] = useState("");
  const [address, setAddress] = useState("");
  const [feedback, setFeedback] = useState<Feedback | null>(null);
  const [pending, startTransition] = useTransition();

  function handleSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setFeedback(null);
    startTransition(async () => {
      const result = await createCinemaAction({ name, timezone, address });
      if (!result.ok) {
        setFeedback({ kind: "error", message: problemMessage(result.error) });
        return;
      }
      setFeedback({
        kind: "success",
        message: `Created cinema ${result.value.id}: ${result.value.name} (${result.value.timezone}).`,
      });
      setName("");
      setTimezone("");
      setAddress("");
      router.refresh();
    });
  }

  return (
    <form className="fields" onSubmit={handleSubmit} aria-label="Create a cinema">
      <label>
        Name
        <input
          type="text"
          name="name"
          value={name}
          onChange={(event) => setName(event.target.value)}
          disabled={pending}
        />
      </label>
      <label>
        Timezone
        <input
          type="text"
          name="timezone"
          value={timezone}
          onChange={(event) => setTimezone(event.target.value)}
          disabled={pending}
        />
      </label>
      <label>
        Address (optional)
        <input
          type="text"
          name="address"
          value={address}
          onChange={(event) => setAddress(event.target.value)}
          disabled={pending}
        />
      </label>
      <button type="submit" disabled={pending}>
        Create cinema
      </button>
      {feedback ? <FeedbackLine feedback={feedback} /> : null}
    </form>
  );
}

/** Creates one room for a cinema chosen from the loaded cinemas. */
export function RoomForm({
  cinemas,
  cinemaId,
}: {
  cinemas: readonly VenueOption[];
  cinemaId: string;
}) {
  const router = useRouter();
  const [selectedCinema, setSelectedCinema] = useState(cinemaId);
  const [name, setName] = useState("");
  const [feedback, setFeedback] = useState<Feedback | null>(null);
  const [pending, startTransition] = useTransition();

  function handleSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setFeedback(null);
    startTransition(async () => {
      const result = await createRoomAction({
        cinemaId: selectedCinema,
        name,
      });
      if (!result.ok) {
        setFeedback({ kind: "error", message: problemMessage(result.error) });
        return;
      }
      setFeedback({
        kind: "success",
        message: `Created room ${result.value.id}: ${result.value.name} in cinema ${result.value.cinemaId}.`,
      });
      setName("");
      router.push(
        `${ADMIN_VENUES_PATH}?cinemaId=${encodeURIComponent(selectedCinema)}`,
      );
      router.refresh();
    });
  }

  return (
    <form className="fields" onSubmit={handleSubmit} aria-label="Create a room">
      <label>
        Cinema
        <select
          name="cinemaId"
          value={selectedCinema}
          onChange={(event) => setSelectedCinema(event.target.value)}
          disabled={pending}
        >
          {cinemas.map((cinema) => (
            <option key={cinema.id} value={cinema.id}>
              {cinema.label}
            </option>
          ))}
        </select>
      </label>
      <label>
        Room name
        <input
          type="text"
          name="name"
          value={name}
          onChange={(event) => setName(event.target.value)}
          disabled={pending}
        />
      </label>
      <button type="submit" disabled={pending}>
        Create room
      </button>
      {feedback ? <FeedbackLine feedback={feedback} /> : null}
    </form>
  );
}

/** Creates the next draft layout version for a chosen room. */
export function LayoutForm({
  rooms,
  roomId,
}: {
  rooms: readonly VenueOption[];
  roomId: string;
}) {
  const router = useRouter();
  const [selectedRoom, setSelectedRoom] = useState(roomId);
  const [feedback, setFeedback] = useState<Feedback | null>(null);
  const [pending, startTransition] = useTransition();

  function handleSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setFeedback(null);
    startTransition(async () => {
      const result = await createLayoutAction({ roomId: selectedRoom });
      if (!result.ok) {
        setFeedback({ kind: "error", message: problemMessage(result.error) });
        return;
      }
      setFeedback({
        kind: "success",
        message: `Created layout ${result.value.id} version ${result.value.version} (${result.value.status}) in room ${result.value.roomId}.`,
      });
      router.refresh();
    });
  }

  return (
    <form className="fields" onSubmit={handleSubmit} aria-label="Create a layout">
      <label>
        Room
        {rooms.length === 0 ? (
          <input
            type="text"
            name="roomId"
            value={selectedRoom}
            onChange={(event) => setSelectedRoom(event.target.value)}
            disabled={pending}
          />
        ) : (
          <select
            name="roomId"
            value={selectedRoom}
            onChange={(event) => setSelectedRoom(event.target.value)}
            disabled={pending}
          >
            {rooms.map((room) => (
              <option key={room.id} value={room.id}>
                {room.label}
              </option>
            ))}
          </select>
        )}
      </label>
      <button type="submit" disabled={pending}>
        Create draft layout
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
