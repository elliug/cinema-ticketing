"use client";

// Local administrative venue forms (M3.5 · T8 · S3).
//
// These components are presentation only: the form text is handed to the server actions in
// ./actions.ts, which validate it, call the API, and return a typed result. A problem result
// is rendered inline and never thrown, so a rejected write such as a duplicate room name
// stays visible instead of breaking the page.
//
// The refusals are printed, not paraphrased: the code, the HTTP status and the API's own
// `detail` — which names the field it refused — appear exactly as the API sent them.
import { useRouter } from "next/navigation";
import { useId, useState, useTransition } from "react";

import { Alert, AlertDescription } from "@/components/ui/alert";
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

/** A confirmation or a refusal, announced politely or interruptively as it deserves. */
function FeedbackLine({ feedback }: { feedback: Feedback }) {
  const error = feedback.kind === "error";
  return (
    <Alert variant={error ? "destructive" : "success"} role={error ? "alert" : "status"}>
      <AlertDescription className="m-0">{feedback.message}</AlertDescription>
    </Alert>
  );
}

/** The API's refusal, printed unchanged. */
function problemMessage(problem: AdminProblem): string {
  return `${problem.code}: ${problem.detail}`;
}

/** Creates one cinema. */
export function CinemaForm() {
  const router = useRouter();
  const ids = useIds("cinema");
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
        message: `Created ${result.value.name} (${result.value.timezone}). It can hold rooms now.`,
      });
      setName("");
      setTimezone("");
      setAddress("");
      router.refresh();
    });
  }

  return (
    <form className="flex flex-col gap-4" onSubmit={handleSubmit} aria-label="Create a cinema">
      <div className="flex flex-wrap items-end gap-4">
        <div className="flex min-w-56 flex-col gap-1.5">
          <Label htmlFor={ids.name}>Name</Label>
          <Input
            id={ids.name}
            name="name"
            value={name}
            onChange={(event) => setName(event.target.value)}
            disabled={pending}
          />
        </div>
        <div className="flex min-w-56 flex-col gap-1.5">
          <Label htmlFor={ids.timezone}>Timezone</Label>
          <Input
            id={ids.timezone}
            name="timezone"
            value={timezone}
            onChange={(event) => setTimezone(event.target.value)}
            disabled={pending}
          />
        </div>
        <div className="flex min-w-56 flex-col gap-1.5">
          <Label htmlFor={ids.address}>Address (optional)</Label>
          <Input
            id={ids.address}
            name="address"
            value={address}
            onChange={(event) => setAddress(event.target.value)}
            disabled={pending}
          />
        </div>
        <Button type="submit" disabled={pending}>
          {pending ? "Creating…" : "Create cinema"}
        </Button>
      </div>
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
  const ids = useIds("room");
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
        message: `Created ${result.value.name} in ${cinemaLabel(cinemas, result.value.cinemaId)}.`,
      });
      setName("");
      router.push(
        `${ADMIN_VENUES_PATH}?cinemaId=${encodeURIComponent(selectedCinema)}`,
      );
      router.refresh();
    });
  }

  return (
    <form className="flex flex-col gap-4" onSubmit={handleSubmit} aria-label="Create a room">
      <div className="flex flex-wrap items-end gap-4">
        <div className="flex min-w-56 flex-col gap-1.5">
          <Label htmlFor={ids.cinema}>Cinema</Label>
          <Select
            value={selectedCinema}
            onValueChange={setSelectedCinema}
            disabled={pending || cinemas.length === 0}
          >
            <SelectTrigger id={ids.cinema}>
              <SelectValue placeholder="Choose a cinema" />
            </SelectTrigger>
            <SelectContent>
              {cinemas.map((cinema) => (
                <SelectItem key={cinema.id} value={cinema.id}>
                  {cinema.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="flex min-w-56 flex-col gap-1.5">
          <Label htmlFor={ids.name}>Room name</Label>
          <Input
            id={ids.name}
            name="name"
            value={name}
            onChange={(event) => setName(event.target.value)}
            disabled={pending}
          />
        </div>
        <Button type="submit" disabled={pending || selectedCinema === ""}>
          {pending ? "Creating…" : "Create room"}
        </Button>
      </div>
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
  const ids = useIds("layout");
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
        message: `Created version ${result.value.version} of the layout in ${roomLabel(rooms, result.value.roomId)}. It is a draft until it is published.`,
      });
      router.refresh();
    });
  }

  return (
    <form className="flex flex-col gap-4" onSubmit={handleSubmit} aria-label="Create a layout">
      <div className="flex flex-wrap items-end gap-4">
        <div className="flex min-w-56 flex-col gap-1.5">
          <Label htmlFor={ids.room}>Room</Label>
          <Select
            value={selectedRoom}
            onValueChange={setSelectedRoom}
            disabled={pending || rooms.length === 0}
          >
            <SelectTrigger id={ids.room}>
              <SelectValue placeholder="Choose a room" />
            </SelectTrigger>
            <SelectContent>
              {rooms.map((room) => (
                <SelectItem key={room.id} value={room.id}>
                  {room.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <Button type="submit" disabled={pending || selectedRoom === ""}>
          {pending ? "Creating…" : "Create draft layout"}
        </Button>
      </div>
      {feedback ? <FeedbackLine feedback={feedback} /> : null}
    </form>
  );
}

function cinemaLabel(cinemas: readonly VenueOption[], id: string): string {
  return cinemas.find((cinema) => cinema.id === id)?.label ?? `cinema ${id}`;
}

function roomLabel(rooms: readonly VenueOption[], id: string): string {
  return rooms.find((room) => room.id === id)?.label ?? `room ${id}`;
}

/** A stable id set for the controls of one form. */
function useIds(prefix: string) {
  const base = useId();
  return {
    name: `${base}-${prefix}-name`,
    timezone: `${base}-${prefix}-timezone`,
    address: `${base}-${prefix}-address`,
    cinema: `${base}-${prefix}-cinema`,
    room: `${base}-${prefix}-room`,
  };
}
