// Package adminapi exposes the local venue, layout and screening administration
// HTTP surface. It must be mounted behind the local-development guard.
//
// The price of a screening is per seat: an order's amount is price x seats, and a
// payment attempt's amount is fixed when it is created, so changing a price affects
// future orders only. A stored attempt and an issued ticket keep the money actually
// taken.
//
// A screening's status decides whether it can be sold at all: POST /admin/events stores one
// as a draft at no price unless the request names a price or a status, and the public
// catalogue answers only an on_sale screening whose start is still ahead. So a create that
// names on_sale, and PATCH /admin/events/{eventId}, are the two ways a screening reaches the
// catalogue. What each status means for the buyer-facing surface is documented on the update
// handler, in events.go.
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
	ListCinemas(ctx context.Context) ([]venue.Cinema, error)
	CreateRoom(ctx context.Context, cinemaID, name string) (venue.Room, error)
	ListRooms(ctx context.Context, cinemaID string) ([]venue.Room, error)
	CreateLayout(ctx context.Context, roomID string) (venue.Layout, error)
	ListLayouts(ctx context.Context, roomID string) ([]venue.Layout, error)
	SetRows(ctx context.Context, layoutID string, rows []venue.Row) (venue.Layout, error)
	PublishLayout(ctx context.Context, layoutID string) (venue.Layout, error)
	CloneLayout(ctx context.Context, layoutID string) (venue.Layout, error)
	GetLayout(ctx context.Context, layoutID string) (venue.Layout, error)
	CreateFilm(ctx context.Context, title string, runtimeMinutes int, rating string) (venue.Film, error)
	ListFilms(ctx context.Context) ([]venue.Film, error)
	CreateEvent(ctx context.Context, roomID, filmID, layoutVersionID string, startsAt time.Time, priceCents *int64, status *string) (venue.Event, error)
	ListEventSeats(ctx context.Context, eventID string) ([]venue.EventSeat, error)
	ListEvents(ctx context.Context) ([]venue.Event, error)
	GetEvent(ctx context.Context, eventID string) (venue.Event, error)
	// UpdateEvent writes the price, the status, or both of one screening; a nil field was
	// not sent and leaves its stored column untouched.
	UpdateEvent(ctx context.Context, eventID string, update venue.EventUpdate) (venue.Event, error)
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
	mux.HandleFunc("GET /cinemas", h.listCinemas)
	mux.HandleFunc("POST /rooms", h.createRoom)
	mux.HandleFunc("GET /cinemas/{cinemaId}/rooms", h.listRooms)
	mux.HandleFunc("POST /layouts", h.createLayout)
	mux.HandleFunc("GET /rooms/{roomId}/layouts", h.listLayouts)
	mux.HandleFunc("PUT /layouts/{layoutId}/rows", h.setRows)
	mux.HandleFunc("POST /layouts/{layoutId}/publish", h.publishLayout)
	mux.HandleFunc("POST /layouts/{layoutId}/clone", h.cloneLayout)
	mux.HandleFunc("GET /layouts/{layoutId}", h.getLayout)
	mux.HandleFunc("POST /films", h.createFilm)
	mux.HandleFunc("GET /films", h.listFilms)
	mux.HandleFunc("POST /events", h.createEvent)
	mux.HandleFunc("GET /events", h.listEvents)
	mux.HandleFunc("GET /events/{eventId}", h.getEvent)
	mux.HandleFunc("PATCH /events/{eventId}", h.updateEvent)
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

// createEventRequest is the create body. The three identifiers and the start are required;
// priceCents and status are optional and are read raw, so a field the request did not send
// stays distinguishable from one it sent as zero (a free screening) and so a value of the
// wrong JSON type is refused by the same parser the update uses, naming its field.
type createEventRequest struct {
	RoomID          string          `json:"roomId"`
	FilmID          string          `json:"filmId"`
	LayoutVersionID string          `json:"layoutVersionId"`
	StartsAt        time.Time       `json:"startsAt"`
	PriceCents      json.RawMessage `json:"priceCents"`
	Status          json.RawMessage `json:"status"`
}

func (h *Handler) createCinema(w http.ResponseWriter, r *http.Request) {
	var req createCinemaRequest
	if !decode(w, r, &req) {
		return
	}
	cinema, err := h.svc.CreateCinema(r.Context(), req.Name, req.Timezone, req.Address)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, cinemaJSON(cinema))
}

func (h *Handler) listCinemas(w http.ResponseWriter, r *http.Request) {
	cinemas, err := h.svc.ListCinemas(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(cinemas))
	for _, cinema := range cinemas {
		items = append(items, cinemaJSON(cinema))
	}
	writeJSON(w, http.StatusOK, map[string]any{"cinemas": items})
}

func (h *Handler) listRooms(w http.ResponseWriter, r *http.Request) {
	cinemaID := r.PathValue("cinemaId")
	rooms, err := h.svc.ListRooms(r.Context(), cinemaID)
	if err != nil {
		writeError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(rooms))
	for _, room := range rooms {
		items = append(items, roomJSON(room))
	}
	writeJSON(w, http.StatusOK, map[string]any{"cinemaId": cinemaID, "rooms": items})
}

func (h *Handler) listLayouts(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	layouts, err := h.svc.ListLayouts(r.Context(), roomID)
	if err != nil {
		writeError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(layouts))
	for _, layout := range layouts {
		items = append(items, map[string]any{"id": layout.ID, "version": layout.Version, "status": layout.Status})
	}
	writeJSON(w, http.StatusOK, map[string]any{"roomId": roomID, "layouts": items})
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
	writeJSON(w, http.StatusCreated, roomJSON(room))
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
	writeJSON(w, http.StatusCreated, filmJSON(film))
}

// listFilms answers every film the administration may schedule a screening against. A
// screen that offers a film to pick cannot offer what it cannot list.
//
// The order is the store's own (title, then identifier): the handler does not re-sort a
// repertoire the database already ordered, and it does not page a list no screen has
// asked to page yet.
func (h *Handler) listFilms(w http.ResponseWriter, r *http.Request) {
	films, err := h.svc.ListFilms(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(films))
	for _, film := range films {
		items = append(items, filmJSON(film))
	}
	writeJSON(w, http.StatusOK, map[string]any{"films": items})
}

// createEvent schedules a screening, optionally already priced and on sale, so putting one
// on sale is one request instead of a create followed by a patch. A body that names neither
// optional field behaves exactly as it always did — a draft at no price — because that is the
// request the scheduling form and the gate send today.
//
// Both fields are validated by the rule the update applies (venue.ValidateEventFields), and a
// refusal names its field and happens before the service and SQL, so a refused create stores
// nothing.
func (h *Handler) createEvent(w http.ResponseWriter, r *http.Request) {
	var req createEventRequest
	if !decode(w, r, &req) {
		return
	}
	priceCents, status, ok := decodeEventPricing(w, req.PriceCents, req.Status)
	if !ok {
		return
	}
	if err := venue.ValidateEventFields(priceCents, status); err != nil {
		writeError(w, err)
		return
	}
	event, err := h.svc.CreateEvent(r.Context(), req.RoomID, req.FilmID, req.LayoutVersionID, req.StartsAt,
		priceCents, status)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, eventJSON(event))
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

// cinemaJSON is the wire shape of a cinema, shared by every cinema response so
// the create and list endpoints cannot drift apart.
func cinemaJSON(cinema venue.Cinema) map[string]any {
	return map[string]any{"id": cinema.ID, "name": cinema.Name, "timezone": cinema.Timezone, "address": cinema.Address}
}

// roomJSON is the wire shape of a room, shared by every room response.
func roomJSON(room venue.Room) map[string]any {
	return map[string]any{"id": room.ID, "cinemaId": room.CinemaID, "name": room.Name}
}

// filmJSON is the wire shape of a film, shared by the create and the list so the two
// cannot answer different fields for the same stored film.
func filmJSON(film venue.Film) map[string]any {
	return map[string]any{"id": film.ID, "title": film.Title, "runtimeMinutes": film.RuntimeMinutes, "rating": film.Rating}
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAdminBodyBytes)).Decode(dst); err != nil {
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
