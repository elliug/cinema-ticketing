// Package adminapi exposes the M1 venue and layout administrative HTTP
// surface. It must be mounted behind the local-development guard.
package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"cinema.local/ticketing/apps/api/internal/venue"
)

// VenueService is the administrative application service contract.
type VenueService interface {
	CreateCinema(ctx context.Context, name, timezone, address string) (venue.Cinema, error)
	CreateRoom(ctx context.Context, cinemaID, name string) (venue.Room, error)
	CreateLayout(ctx context.Context, roomID string) (venue.Layout, error)
	SetRows(ctx context.Context, layoutID string, rows []venue.Row) (venue.Layout, error)
	PublishLayout(ctx context.Context, layoutID string) (venue.Layout, error)
	CloneLayout(ctx context.Context, layoutID string) (venue.Layout, error)
	GetLayout(ctx context.Context, layoutID string) (venue.Layout, error)
	CreateFilm(ctx context.Context, title string, runtimeMinutes int, rating string) (venue.Film, error)
	CreateEvent(ctx context.Context, roomID, filmID, layoutVersionID string, startsAt time.Time) (venue.Event, error)
	ListEventSeats(ctx context.Context, eventID string) ([]venue.EventSeat, error)
}

// Handler serves the administrative routes.
type Handler struct {
	svc VenueService
}

// New builds a Handler over the given service.
func New(svc VenueService) *Handler { return &Handler{svc: svc} }

// Mux returns the administrative route table.
func (h *Handler) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /cinemas", h.createCinema)
	mux.HandleFunc("POST /rooms", h.createRoom)
	mux.HandleFunc("POST /layouts", h.createLayout)
	mux.HandleFunc("PUT /layouts/{layoutId}/rows", h.setRows)
	mux.HandleFunc("POST /layouts/{layoutId}/publish", h.publishLayout)
	mux.HandleFunc("POST /layouts/{layoutId}/clone", h.cloneLayout)
	mux.HandleFunc("GET /layouts/{layoutId}", h.getLayout)
	mux.HandleFunc("POST /films", h.createFilm)
	mux.HandleFunc("POST /events", h.createEvent)
	mux.HandleFunc("GET /events/{eventId}/seats", h.listEventSeats)
	return mux
}

type createCinemaRequest struct {
	Name     string `json:"name"`
	Timezone string `json:"timezone"`
	Address  string `json:"address"`
}

type createRoomRequest struct {
	CinemaID string `json:"cinemaId"`
	Name     string `json:"name"`
}

type createLayoutRequest struct {
	RoomID string `json:"roomId"`
}

type setRowsRequest struct {
	Rows []venue.Row `json:"rows"`
}

type createFilmRequest struct {
	Title          string `json:"title"`
	RuntimeMinutes int    `json:"runtimeMinutes"`
	Rating         string `json:"rating"`
}

type createEventRequest struct {
	RoomID          string    `json:"roomId"`
	FilmID          string    `json:"filmId"`
	LayoutVersionID string    `json:"layoutVersionId"`
	StartsAt        time.Time `json:"startsAt"`
}

func (h *Handler) createCinema(w http.ResponseWriter, r *http.Request) {
	var req createCinemaRequest
	if !decode(w, r, &req) {
		return
	}
	c, err := h.svc.CreateCinema(r.Context(), req.Name, req.Timezone, req.Address)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": c.ID, "name": c.Name, "timezone": c.Timezone, "address": c.Address})
}

func (h *Handler) createRoom(w http.ResponseWriter, r *http.Request) {
	var req createRoomRequest
	if !decode(w, r, &req) {
		return
	}
	room, err := h.svc.CreateRoom(r.Context(), req.CinemaID, req.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": room.ID, "cinemaId": room.CinemaID, "name": room.Name})
}

func (h *Handler) createLayout(w http.ResponseWriter, r *http.Request) {
	var req createLayoutRequest
	if !decode(w, r, &req) {
		return
	}
	layout, err := h.svc.CreateLayout(r.Context(), req.RoomID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeLayout(w, http.StatusCreated, layout)
}

func (h *Handler) setRows(w http.ResponseWriter, r *http.Request) {
	var req setRowsRequest
	if !decode(w, r, &req) {
		return
	}
	layout, err := h.svc.SetRows(r.Context(), r.PathValue("layoutId"), req.Rows)
	if err != nil {
		writeError(w, err)
		return
	}
	writeLayout(w, http.StatusOK, layout)
}

func (h *Handler) publishLayout(w http.ResponseWriter, r *http.Request) {
	layout, err := h.svc.PublishLayout(r.Context(), r.PathValue("layoutId"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeLayout(w, http.StatusOK, layout)
}

func (h *Handler) cloneLayout(w http.ResponseWriter, r *http.Request) {
	layout, err := h.svc.CloneLayout(r.Context(), r.PathValue("layoutId"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeLayout(w, http.StatusCreated, layout)
}

func (h *Handler) getLayout(w http.ResponseWriter, r *http.Request) {
	layout, err := h.svc.GetLayout(r.Context(), r.PathValue("layoutId"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeLayout(w, http.StatusOK, layout)
}

func (h *Handler) createFilm(w http.ResponseWriter, r *http.Request) {
	var req createFilmRequest
	if !decode(w, r, &req) {
		return
	}
	film, err := h.svc.CreateFilm(r.Context(), req.Title, req.RuntimeMinutes, req.Rating)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": film.ID, "title": film.Title, "runtimeMinutes": film.RuntimeMinutes, "rating": film.Rating})
}

func (h *Handler) createEvent(w http.ResponseWriter, r *http.Request) {
	var req createEventRequest
	if !decode(w, r, &req) {
		return
	}
	event, err := h.svc.CreateEvent(r.Context(), req.RoomID, req.FilmID, req.LayoutVersionID, req.StartsAt)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": event.ID, "roomId": event.RoomID, "filmId": event.FilmID,
		"layoutVersionId": event.LayoutVersionID, "startsAt": event.StartsAt, "status": event.Status,
	})
}

func (h *Handler) listEventSeats(w http.ResponseWriter, r *http.Request) {
	seats, err := h.svc.ListEventSeats(r.Context(), r.PathValue("eventId"))
	if err != nil {
		writeError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(seats))
	for _, seat := range seats {
		items = append(items, map[string]any{"id": seat.ID, "layoutSeatId": seat.LayoutSeatID, "state": seat.State})
	}
	writeJSON(w, http.StatusOK, map[string]any{"eventId": r.PathValue("eventId"), "seats": items})
}

func writeLayout(w http.ResponseWriter, status int, layout venue.Layout) {
	writeJSON(w, status, layout)
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(dst); err != nil {
		writeJSON(w, http.StatusBadRequest, problem{
			Type: "https://cinema.local/problems/invalid-request", Title: "Invalid request",
			Status: http.StatusBadRequest, Code: "invalid_request", Detail: "request body must be valid JSON",
		})
		return false
	}
	return true
}

type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// writeError maps domain errors to RFC 9457 problem details.
func writeError(w http.ResponseWriter, err error) {
	var invalid *venue.InvalidError
	switch {
	case errors.As(err, &invalid):
		writeJSON(w, http.StatusBadRequest, problem{
			Type: "https://cinema.local/problems/invalid-input", Title: "Invalid input",
			Status: http.StatusBadRequest, Code: "invalid_input", Detail: invalid.Error(),
		})
	case errors.Is(err, venue.ErrNotFound):
		writeJSON(w, http.StatusNotFound, problem{
			Type: "https://cinema.local/problems/not-found", Title: "Not found",
			Status: http.StatusNotFound, Code: "not_found", Detail: "the requested resource does not exist",
		})
	case errors.Is(err, venue.ErrConflict):
		writeJSON(w, http.StatusConflict, problem{
			Type: "https://cinema.local/problems/conflict", Title: "Conflict",
			Status: http.StatusConflict, Code: "conflict", Detail: err.Error(),
		})
	default:
		writeJSON(w, http.StatusInternalServerError, problem{
			Type: "https://cinema.local/problems/internal", Title: "Internal error",
			Status: http.StatusInternalServerError, Code: "internal_error", Detail: "the request could not be completed",
		})
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		_, _ = fmt.Fprintln(w, err)
	}
}
