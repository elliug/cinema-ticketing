// Site footer (M3.5 · T3 · S2, S8).
//
// What this build is, said once, in the one place a reader looks when they want to know
// whether they are using something real. Three facts and no marketing:
//
//   1. this is a local development build, not a service;
//   2. tickets and reservations are opened by their identifier — a forwarded link works for
//      whoever holds it, by design (milestone log, L3);
//   3. signing in is real and optional: a session is what makes "these are your tickets"
//      something the box office answers instead of something this browser remembers, and
//      nothing in the purchase path requires one (identity-login T6 · D3, D4, S7).
//
// The demonstration session this paragraph used to describe is gone: a fake session labelled
// beside a real one is a claim that the fake one authenticates somebody, and S7 retired it.
//
// No `"use client"`: the footer has no behaviour.

export function SiteFooter() {
  return (
    <footer className="site-footer">
      <div className="site-footer__inner">
        <p className="site-footer__heading">Desarrollo local · no es un servicio</p>
        <p>
          Esta aplicación web va delante de la API en Go de este repositorio y corre en esta
          máquina. No se vende nada a nadie.
        </p>
        <p>
          Las entradas y las reservas se abren por su identificador, así que cualquiera que
          tenga un enlace las ve: es así a propósito y está registrado en el diario del
          proyecto. No hay ninguna sesión de demostración: iniciar sesión es real, es opcional,
          y nada del proceso de compra lo pide.
        </p>
      </div>
    </footer>
  );
}
