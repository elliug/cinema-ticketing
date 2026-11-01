# Email delivery: tickets by mail

Estudio previo a la implementación. Tres decisiones ya tomadas por el responsable del producto, y
un conjunto de restricciones que el código existente impone y que no son negociables sin
reescribir contratos documentados.

Estado: **entregado y en `main`** como seis PRs encadenadas. Queda una sola cosa sin cerrar, y está
escrita abajo: el lente de resiliencia de la revisión nativa de la PR del gate nunca emitió un
resultado admisible, y el responsable del producto decidió fusionar documentándolo.

## Decisiones tomadas

- **D1 — Se pide en el checkout, antes de pagar.** El comprador que cierra la pestaña igual recibe
  sus entradas.
- **D2 — Solo se guarda la dirección, sin verificar.** No hay doble opt-in: se envía directamente.
- **D3 — El correo lleva un enlace firmado y caducable**, no los enlaces permanentes de las
  entradas.

## Lo que el código ya impone

Estas cuatro cosas no son opinión: están escritas en el repositorio y condicionan el diseño.

1. **`published_at` no es "este consumidor terminó".** El comentario de
   `apps/api/internal/pdf/outbox.go:20-28` dice literalmente que la marca significa *"every
   consumer of this topic has acted"*, que hoy el worker de PDF es el único consumidor, y que
   **"a later relay … must not assume `published_at IS NULL` on its own, and needs its own cursor
   or a separate delivery record"**. Es decir: el segundo consumidor necesita su propio registro
   de entrega; reutilizar la marca rompería el significado para el primero.
2. **El payload del outbox no lleva dirección.** `TicketIssued` es
   `{reservationId, eventId, attemptId, amountCents, ticketIds, seatIds}`. La dirección tiene que
   estar almacenada en otro sitio y resolverse por `reservationId`. Meterla en el payload
   duplicaría PII en una cola durable.
3. **No existe ninguna columna de email en el esquema**, y `ARCHITECTURE.md:208` ya describe el
   email como *consumidor* del outbox, no como mecanismo nuevo.
4. **Cuatro textos afirman hoy que no se envía correo**, y pasan a ser falsos en este mismo
   cambio: `GUIDE.md:132`, `ARCHITECTURE.md:208`, `apps/web/src/app/tickets/page.tsx:73` y
   `apps/web/src/app/tickets/[ticketID]/page.tsx:112` ("The PDF is what you get and nothing is
   mailed").

## Specs

- **S1 — La dirección se pide en el checkout y se guarda antes de llamar al proveedor.** Si el
  proceso muere durante el cobro, la fila del intento ya existe *y* la dirección ya existe: el
  worker nunca encuentra una venta sin destinatario.
- **S2 — Sin verificación, y se dice.** Se envía a la dirección escrita, sin comprobar que sea de
  quien la escribe. La consecuencia (cualquiera puede escribir la dirección de otro) se documenta
  como límite honesto, no se esconde.
- **S3 — El correo lleva un enlace firmado con caducidad.** Firmado con HMAC y con `exp`
  explícito; pasada la fecha, el enlace deja de abrir. No es una llave permanente en un buzón.
- **S4 — La entrega es un consumidor del outbox con su propio registro.** No toca `published_at`;
  escribe una fila por `(consumidor, fila)` y es idempotente: reenviar el mismo tick no manda dos
  correos.
- **S5 — La venta no depende del correo.** Sin proveedor configurado el consumidor no arranca, y
  con el proveedor caído la venta se completa igual: el envío reintenta en el siguiente tick.
  **Los gates siguen verdes sin servidor de correo.**
- **S6 — Los cuatro textos que dicen "nothing is mailed" se corrigen en el mismo cambio.**

## Diseño propuesto

### Dónde vive la dirección (D2 obliga a decidir esto primero)

Dos opciones, y la decisión 2 inclina la balanza:

| Opción | A favor | En contra |
|---|---|---|
| `cinema.reservations.contact_email` (nullable) | Muere con la reserva; una compra = una fila; sin identidad inventada | Se aleja de `ARCHITECTURE.md:98`, que prevé `guest_customers` |
| `cinema.guest_customers` + `reservations.customer_id` | Es el destino documentado; `verified_at` preparado para el login | Con D2, `verified_at` sería siempre `NULL`: una columna que miente sobre lo que el sistema garantiza |

**Recomendación: la columna en `reservations`, ahora.** Un registro de cliente implica una
identidad que este producto no tiene todavía, y con "solo guardar" la columna `verified_at` sería
decorativa. Cuando llegue el login (que sí verificará), se promociona a `guest_customers` y la
columna se convierte en `customer_id`, con migración. Esto **se aparta de `ARCHITECTURE.md:98`** y
por eso queda escrito aquí: es una corrección consciente al plan, no un olvido.

Retención, en la misma línea: la dirección solo tiene sentido mientras exista una venta que
entregar. El barrido de expiración ya toca las reservas caducadas; ahí se puede vaciar la dirección
de un hold que nunca se pagó, para no acumular PII de compras abandonadas.

### El consumidor (S4, S5)

```
RecordOutcome (transacción) ──► cinema.outbox (ticket.issued)        ← ya existe
                                      │
                                      ▼
                    worker de email: lee filas del tema, salta las que
                    ya tienen entrega para este consumidor
                                      │
                                      ├─ sin dirección  → marca "omitido" y loguea
                                      ├─ envía vía SMTP/Resend
                                      └─ éxito → INSERT outbox_deliveries(consumer, outbox_id)
```

- **Tabla nueva**: `cinema.outbox_deliveries(consumer text, outbox_id uuid, delivered_at timestamptz,
  PRIMARY KEY (consumer, outbox_id))`. Es la "separate delivery record" que el comentario del
  outbox pide.
- **Worker**: copia del patrón de `pdf.Worker` — lote acotado, timeout por tick, log del fallo y
  siguiente tick. No reintenta en bucle cerrado.
- **Arranque condicional**: solo se construye si hay proveedor configurado (`API_SMTP_URL` o clave
  del proveedor). Sin él, `cmd/api` no lo arranca y los gates no lo notan.

### El enlace firmado (S3)

Se reutiliza la maquinaria del QR (`apps/api/internal/tickets/qr.go`): HMAC-SHA256 sobre un payload
canónico y `base64url`.

- Payload: `v1|reservationId|<exp>`, con `exp` en el mismo formato que el resto del proyecto (UTC).
- Clave **distinta** de la del QR (`API_TICKET_LINK_KEY`): rotar el enlace de un correo viejo no
  debe invalidar los códigos impresos, ni al revés.
- Ruta: `/t/<token>`, resuelta en el servidor web; valida firma, caducidad y que la reserva esté
  pagada; renderiza las entradas y **las añade a la lista de la pestaña** (que es lo que ya hace
  abrir una entrada). Así el enlace sirve una vez y la pestaña se queda con ellas.
- Caducidad por defecto para la demo: **7 días**, configurable. Se documenta que un correo viejo
  deja de abrir, y eso es deliberado.

### La UI (D1)

- **Checkout** (`/reservations/[reservationID]`): campo `type="email"` opcional, con la frase
  honesta: "te enviamos un enlace que abre tus entradas; cualquiera con ese enlace puede abrirlas".
  Opcional y no obligatorio porque forzarlo almacena PII de quien abandona la compra.
- **Pantalla de la entrada y lista**: acción "enviar por email" que acepta una dirección en el
  momento (para reenviar a otra persona) sin guardarla como segunda dirección de la reserva.
  Limitada por reserva para no ser un relé de spam.

## Trampas y límites que este diseño no resuelve

- **Sin verificación (D2), cualquiera puede escribir la dirección de otro.** Para una demo es
  aceptable si está escrito; en producción no lo es, y el camino documentado (`guest_customers` con
  `verified_at`) es la respuesta cuando importe.
- **No hay gestión de rebotes.** Un correo que rebota se queda como enviado. Se acepta y se dice.
- **El enlace caduca**: un usuario que vuelve a abrir el correo un mes después no entra. Es la
  consecuencia de no poner una credencial permanente en un buzón.
- **PII nueva en la base de datos** (aunque sea un demo): con el login llega el RGPD de verdad;
  hoy basta con retención y con no duplicar la dirección en el payload del outbox.
- **Un reenvío no reenvía el PDF**: el enlace abre las entradas en el navegador; el PDF sigue
  descargándose desde la web, como hoy.

## Tareas previstas

| # | Tarea | Specs |
|---|---|---|
| T1 | Migración: `reservations.contact_email` + `outbox_deliveries` | S1, S4 |
| T2 | API: aceptar y guardar la dirección en el intento de pago; validación y `ErrInvalidInput` | S1 |
| T3 | Consumidor de email (worker + registro de entrega) y Mailpit en `compose.yaml` | S4, S5 |
| T4 | Enlace firmado `/t/<token>` con caducidad y su clave | S3 |
| T5 | UI: campo en checkout + acción de reenvío | S1, D1 |
| T6 | Corregir los cuatro textos que dicen que no se envía nada | S6 |
| T7 | Gates: `verify-m3` con y sin proveedor; gated test del consumidor | S5 |

Esfuerzo estimado: dos a tres días, con T1–T3 como primer corte (el correo ya sale por Mailpit) y
T4–T7 como segundo.

**Estado: T1–T7 entregados.** T5 se partió en sus dos mitades reales (campo del checkout y acción de
reenvío) porque la segunda abre una ruta pública que envía correo a una dirección arbitraria y
necesita la limitación anti-relé probada por separado. T7 se entregó como gate propio
(`verify-email`), no tocando `verify-m3`.

## Slices

- **Slice 1 — este documento** (`docs(odd): study email delivery`): 162 líneas. Las decisiones y
  las restricciones que el código impone, antes de escribir código.
- **Slice 2 — esquema y captura** (`feat(checkout): carry the address the tickets are mailed
  to`): 480 líneas. Migración 0009 en ambos sentidos, la dirección guardada en la fase 1, el
  cuerpo opcional de la ruta de pago y 6 tests. Queda por encima del objetivo de ~400 líneas y
  es deliberado: la migración, la escritura en la transacción y los tests que prueban el orden
  son **un solo comportamiento**; separarlos daría un commit que compila pero no prueba nada.
- **Slice 3 — el consumidor** (`feat(email): a ticket email consumer with its own delivery
  record`): 713 líneas. El paquete `email` completo (worker, store, adaptador SMTP) y 7
  comportamientos probados. Por encima del objetivo y por la misma razón que el slice 2: el
  worker, su consulta y sus bordes (sin dirección, envío fallido, fila ilegible) son **un**
  comportamiento; trocearlo daría commits que compilan y no prueban nada. **Todavía no envía
  nada**: sin config ni cableado en `cmd/api`, sin Mailpit en compose y sin test gated contra
  PostgreSQL.
- **Slice 4 — el cableado y el enlace firmado**: tres commits, 813 líneas.
  - `feat(tickets): a signed link that opens the tickets and then stops working` (359 líneas):
    `tickets.LinkSigner` con clave propia, payload `v1|reservationId|<exp>` en RFC 3339 UTC,
    comparación canónica y sentinel `ErrLinkExpired`; `API_TICKET_LINK_KEY` y
    `API_TICKET_LINK_TTL` (168h por defecto); y `GET /api/v1/ticket-links/{token}`, que abre solo
    una venta pagada y responde un **único** 404 opaco para falsificado, caducado, desconocido e
    impagado.
  - `feat(web): the mailed link lands somewhere that keeps the tickets` (346 líneas):
    `/t/[token]` como componente de servidor que canjea el token en el proceso (el navegador
    nunca conoce la URL de la API), guarda **todas** las entradas en la lista de la pestaña,
    `force-dynamic` y sin indexar, y tres estados decididos en un módulo puro: abierto, cerrado
    (una sola página para caducado y nunca existido) y no disponible (reintentable).
  - `feat(email): start the ticket email consumer beside the PDF one` (108 líneas):
    `API_SMTP_ADDR` + `API_EMAIL_FROM` (nunca medio proveedor) y `API_PUBLIC_WEB_URL`; el segundo
    consumidor arranca solo si hay proveedor, con el enlace firmado y sin bloquear nunca una
    venta (S5); Mailpit en `compose.yaml` bajo el perfil `mail` detrás de `make mail-up`, fuera de
    `deps-up`. El test gated contra PostgreSQL destapó un fallo real: `Recipient` de una reserva
    inexistente respondía «sin dirección», así que el worker habría marcado como entregada una
    venta que no pudo leer; ahora es un error.

  El cableado llevó `API_SMTP_ADDR` en lugar del `API_SMTP_URL` que se propuso aquí: el adaptador
  ya existente pide `host:port`, y un «URL» que no acepta esquema sería un nombre que miente.
- **Slice 5 — la UI y los textos**: cometidos en cinco unidades, 1.610 líneas.
  - `feat(web): ask for the address that the tickets are mailed to, before paying`: el campo
    opcional del checkout y el cuerpo `{"email": …}` (sin cuerpo cuando no se escribe nada),
    con las cinco frases que dicen las condiciones en vez de vender la función.
  - `docs: say what is mailed now, instead of saying nothing is`: los cuatro textos y la fila
    de `GUIDE.md`, más el paso del pago y el párrafo de `ARCHITECTURE.md`.
  - `feat(email): mail the link to an address typed on the ticket page`: `POST
    /api/v1/tickets/{id}/email`, reutilizando el asunto y el cuerpo del worker y la misma
    validación de dirección del checkout; 3 envíos por reserva y hora en Redis, contados antes
    del envío y **fallando cerrado**; 429 con `Retry-After`; 503 cuando no hay proveedor.
  - `feat(web): a ticket page action that mails the link to another address`: el formulario que
    usa esa ruta y dice en palabras cada respuesta, incluido el 404 opaco.
  - `test(gate): prove the ticket email end to end through Mailpit`: `make verify-email` y su
    trabajo de CI, con Mailpit arrancado a propósito.

  **T7 se entregó en la forma correcta, no en la prevista**: `verify-m3` no se tocó (sigue
  corriendo sin proveedor, que es la mitad «sin» de S5) y la mitad «con proveedor» es un gate
  propio, `verify-email`, con su base de datos, índice de Redis, puerto y bucket, que exige
  **exactamente un** mensaje, saca el token del cuerpo, lo canjea por las entradas de la orden y
  comprueba dos ticks después que sigue habiendo uno — con `published_at` ya puesto por el worker
  de PDF y una fila de `outbox_deliveries` para `email`. Esa combinación es exactamente el fallo
  que el registro de entrega existe para evitar, así que el gate lo fija en vez de confiar en el
  comentario. Añadir Mailpit a `verify-m3` habría convertido un servidor de correo en requisito
  de todos los gates.

## Entrega

El trabajo fue en PRs encadenadas, una por grupo de comportamiento, porque el conjunto pasaba de
8.500 líneas y una sola PR de ese tamaño no se revisa. Terminó en seis, y no por gusto: el
proveedor de revisión diffea cada PR contra `main`, así que un candidato encadenado se revisa
**sumado a sus padres** hasta que éstos entran, y por encima de unas 2.600 líneas ni siquiera
arranca (`lens_context_budget_exceeded`). El corte real fue: #97 (slices 1–3, el consumidor),
#100 (el firmante y su ruta), #101 (la página y el cableado), #102 (el campo del checkout y los
textos), #103 (el reenvío y su límite) y #104 (el gate y este documento).

### Revisión nativa

| PR | Lentes | Resultado |
|---|---|---|
| #97 | risk, resilience | aprobada, autoridad quemada |
| #100 | risk, resilience | aprobada, autoridad quemada |
| #101 | risk, resilience | aprobada, autoridad quemada |
| #102 | risk, resilience | aprobada, autoridad quemada (un lente hubo de repetirse) |
| #103 | risk, resilience | aprobada, autoridad quemada |
| #104 | risk, resilience | **risk admitido; resilience nunca admitido** |

En #104 el modelo del lente de resiliencia emite como localización de un hallazgo
`scripts/verify_email.sh:cleanup` — una función, no una línea — y el admittedor la rechaza con
`line_suffix_not_integer` **dos veces**, sin consumir el slot. No es un veredicto sobre el código ni
un fallo que se pueda reparar desde aquí: es el modelo del proveedor produciendo un payload que su
propio contrato rechaza. La autoridad de esa revisión quedó **sin quemar**, y el payload rechazado
conservado por el proveedor en `.git/gentle-ai/rejected-results/`.

Todos los hallazgos admitidos fueron **informativos**, ninguno abrió corrección: el más útil es que
`Recipient` de una reserva inexistente reintenta en cada tick en vez de darse por entregada (es
intencionado: una venta ilegible no se marca como enviada), y que la ruta de reenvío no filtra
caracteres de control en el destinatario — el adaptador SMTP ya los rechaza, de modo que es defensa
en profundidad y no un agujero.

## Log

- **L1** — Petición del usuario, literal: «comencemos con el mail. entiendo que además nos tocará
  extender la ui para solicitar el mail del usuario si no lo tenemos del login o quieren enviarlo a
  otra dirección. Estudiamos todo esto?»
- **L2** — Decisiones del usuario: se pide **en el checkout antes de pagar**; **solo se guarda**,
  sin verificación; el correo lleva un **enlace firmado y caducable**.
- **L3** — Corrección consciente a `ARCHITECTURE.md:98`: con D2 se usa una columna en
  `reservations` en lugar de `guest_customers`, porque `verified_at` sería una columna que afirma
  algo que el sistema no garantiza. Se promociona cuando llegue el login.
- **L4** — El orden respecto al login queda resuelto: esto **es** el camino de invitado que la
  arquitectura describe, no un desvío. El login se apoyará encima sin rehacerlo.
- **L5** — Decisión del usuario sobre la PR del gate, literal: «2». Elige fusionarla **sin** el
  segundo lente, dejando escrito que el gate se validó ejecutándose (verde tres veces, con su job
  de CI) y no por una vista externa completa.
