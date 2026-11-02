# Identidad y login: cuentas de invitado verificadas

Estudio previo a la implementación. Cinco decisiones que solo puede tomar el responsable del
producto, y un conjunto de restricciones que el código existente impone y que no son negociables
sin reescribir contratos documentados.

Estado: **estudio cerrado, decisiones tomadas** (D1–D5, ver L2). No hay código todavía: este
documento es la especificación de la feature.

## Decisiones tomadas

Tomadas por el responsable del producto (L2). Cada una lleva la opción elegida y, debajo, las
alternativas que se descartaron y por qué: el motivo importa tanto como la decisión, porque es lo
que evitará volver a discutirlas.

### D1 — Qué identifica a una persona

| Opción | A favor | En contra |
|---|---|---|
| **Enlace mágico por correo** | Reutiliza lo que ya está en `main`: enlace firmado con caducidad, consumidor de correo y límite por reserva en Redis. No hay contraseñas que guardar, filtrar, resetear ni comparar. **Verifica la dirección por construcción**, que es lo que `verified_at` quiere decir | Depende del correo: si el proveedor está caído, no se entra. Un buzón compartido es una cuenta compartida |
| Google OAuth | Sin correo saliente, sin caducidad, y con el «Entrar con Google» que la gente reconoce | Tercero, credenciales y consentimiento; no verifica nada que el enlace no verifique; y **no reutiliza nada** de lo construido |
| Correo + contraseña | Familiar | Hay que almacenar hashes, política de contraseñas, reset, bloqueo por intentos y protección contra brechas. Todo eso es superficie nueva para un demo |

**Decidido: enlace mágico.** Es la única opción cuyo coste marginal es casi cero, porque el
canal de correo ya existe y es el que verifica. OAuth entra después como **un identificador más de
la misma cuenta**, sin rehacer nada.

### D2 — Qué significa «tener sesión»

| Opción | A favor | En contra |
|---|---|---|
| **Token opaco en base de datos + cookie `HttpOnly`** | Revocable de verdad (borrar la fila cierra la sesión), caducidad explícita, y el navegador no puede leer el token | Una tabla nueva y una escritura por login |
| Cookie firmada sin estado | Sin tabla | No se puede revocar: cerrar sesión es solo borrar la cookie del cliente, y una copia robada sigue valiendo hasta caducar |
| Token en `localStorage` | Trivial de implementar | Cualquier XSS se lleva la sesión, y el navegador tendría que hablar con el API, cosa que hoy **no** hace |

**Decidido: token opaco en base de datos.** La revocación es la propiedad que importa.

### D3 — Qué gana quien entra

Hoy `/tickets` es una **nota del navegador**: la lista de identificadores que esa pestaña guardó
(`apps/web/src/lib/issued-tickets.ts`). Con sesión puede ser una lectura del servidor para la
cuenta, y entonces:

- la lista sobrevive al navegador, al dispositivo y al borrado de datos del sitio;
- deja de haber identificadores de entradas guardados en el navegador;
- y por primera vez el sistema puede decir «estas son *tus* entradas» sin mentir.

**Decidido: sí, y es el único motivo por el que merece la pena hacer login.** Sin esto, una
cuenta sería un formulario que no cambia nada.

### D4 — Qué pasa con la compra de invitado

| Opción | A favor | En contra |
|---|---|---|
| **Seguir comprando sin cuenta** | Es lo que el producto promete hoy: una entrada se abre por su enlace, y quien compra no tiene que registrarse | Nada nuevo que ganar salvo la lista del punto D3 |
| Exigir cuenta para comprar | Una cuenta por comprador | Rompe la promesa de invitado, añade fricción antes de cobrar y contradice `ARCHITECTURE.md` |

**Decidido: la sesión es opcional.** Nadie necesita cuenta ni para comprar ni para abrir una
entrada; entrar añade la lista del punto D3 y nada más.

### D5 — Qué se hace con las compras anteriores a la identidad

Las reservas que ya existen guardan una dirección **nunca verificada** en
`reservations.contact_email`. Cuando alguien verifica esa dirección con un enlace, hay dos lecturas:

- **reclamar** las compras hechas con esa dirección, porque verificar el buzón es exactamente la
  prueba que faltaba; o
- **no reclamar nada**, y que solo las compras nuevas con sesión queden asociadas.

**Decidido: reclamar**, y solo en el momento de la verificación. No es una reescritura de
datos: es atar hacia atrás lo que estaba escrito sin garantía, en el único instante en que la
garantía existe. Se anota en el log y se hace en una transacción.

## Lo que el código ya impone

Estas seis cosas están escritas en el repositorio y condicionan el diseño.

1. **El identificador de la entrada es la capacidad.** `apps/api/internal/publicapi/tickets.go:5-8`
   dice literalmente que «el identificador de la entrada es la capacidad», y
   `reservation.go:15-18` repite lo mismo para la reserva. **Consecuencia honesta: añadir login no
   protege nada de lo ya entregado.** Quien tenga un enlace de entrada lo seguirá abriendo. La
   sesión protege la *lista*, no las entradas ya repartidas.

2. **No existen ni `users` ni `guest_customers`.** `ARCHITECTURE.md:95-110` las documenta como
   estado objetivo (`guest_customers(id, email, phone, verified_at)`) y **ninguna migración las
   crea**. `reservations` no tiene `customer_id`: solo `id`, `event_id`, `status`, `expires_at` y
   `created_at`, más la dirección que añadió `0009`.

3. **La promoción ya está prevista por escrito.** `db/migrations/0009_email_delivery.up.sql:12-14`
   dice que cuando llegue la identidad esa columna pasará a referenciar una fila de
   `guest_customers` «que *sí* lleva verificación, y ese es el momento en que una afirmación de
   verificación se vuelve verdadera». Este estudio es ese momento.

4. **No hay ni una cookie en todo el proyecto**, y el único `Authorization` es el del guard de
   administración local (`apps/api/internal/httpserver/admin_guard.go:33`), que compara un bearer
   en tiempo constante y exige peer loopback. La sesión de usuario es un mecanismo nuevo, no una
   variante del guard, y no debe parecerse a él.

5. **El navegador nunca habla con el API.** La URL base vive en variables de entorno del servidor
   de Next (`resolvePublicConfig(process.env)` en `apps/web/src/app/tickets/page.tsx:55`) y el
   navegador solo habla con acciones de servidor. Es una restricción de diseño, no un accidente:
   significa que la sesión tiene **dos saltos** —la cookie la emite y la lee Next, y Next la
   presenta al API en cada acción de servidor— y que la cookie no puede ser del dominio del API.

6. **La maquinaria reutilizable existe y tiene forma.** `tickets.LinkSigner`
   (`apps/api/internal/tickets/link.go:98-187`) firma `v1|<reservationId>|<exp>` con HMAC-SHA256 y
   caducidad fijada en la construcción; el paquete `email` tiene `Sender`, `Message`, `Subject` y
   el cuerpo ya escritos, y `ResendLimiter` (`email/resend.go:100-107`) es un contador que **falla
   cerrado**. Lo que no encaja es el payload: un enlace de acceso identifica una **dirección**, no
   una reserva, así que necesita su propio firmante y su propia clave, nunca reutilizar
   `API_TICKET_LINK_KEY`.

## Specs

- **S1 — La sesión es un token opaco, con caducidad y revocación.** Se guarda su hash en base de
  datos, nunca el token; la cookie es `HttpOnly`, `SameSite=Lax`, `Secure` fuera de desarrollo, y
  borrar la fila cierra la sesión de inmediato.
- **S2 — Se entra pidiendo un enlace al correo, y el enlace caduca y sirve una sola vez.** Caducidad
  corta (minutos, no días) y consumo registrado: el mismo enlace no abre dos sesiones.
- **S3 — Entrar verifica la dirección.** `guest_customers.verified_at` se escribe en el mismo acto
  que la sesión; no es una columna decorativa. Y es lo que permite el reclamo de D5.
- **S4 — La compra de invitado sigue intacta.** Sin cookie, sin cuenta, el checkout y la entrada se
  comportan exactamente como hoy; ninguna ruta pública pasa a exigir sesión.
- **S5 — Mi lista deja de ser una nota del navegador.** `/tickets` lee del servidor las entradas de
  la cuenta de la sesión, y el navegador deja de guardar identificadores de entradas ajenas. Sin
  sesión, la pantalla dice la verdad: no hay cuenta, y el navegador guarda lo que ya guardaba.
- **S6 — El envío de enlaces está limitado y falla cerrado.** Límite por dirección y por origen,
  con el patrón ya probado en `email.ResendLimiter`: si el contador no responde, se rechaza.
- **S7 — La sesión de demostración desaparece el mismo día.** `apps/web/src/lib/demo-session.ts`
  se retira: `odd/tasks/m35-product-web.md` prohíbe (S2, S8, L3) que ningún documento o interfaz
  presente la demo como autenticación, y tener las dos a la vez sería exactamente eso.
- **S8 — No se reescribe nada de lo existente.** Las reservas antiguas conservan solo la dirección;
  el reclamo de D5 ata compras hacia atrás, no corrige ni inventa datos.

## Diseño propuesto

### El enlace de acceso (D1, S2, S3)

Tercer firmante, con clave propia (`API_IDENTITY_LINK_KEY`, mínimo 32 bytes, valor de desarrollo
solo con `API_ENV=development`, como las otras dos). Payload canónico:

```
v1|access|<email normalizado>|<nonce>|<exp>
```

- `nonce`: identificador de un solo uso, registrado al emitir el enlace. Sin él, dos enlaces
  emitidos en el mismo segundo serían el mismo token.
- `exp`: minutos, no días. Un enlace de acceso es un paso, no una llave.
- El token es `base64url(payload) + "." + base64url(hmac)`, con la misma comprobación canónica que
  `link.go`: se rechaza cualquier payload que no se re-codifique a sí mismo.
- La dirección se normaliza (recorte y minúsculas) **antes** de firmar y después de verificar, para
  que `Ana@x.com` y `ana@x.com` sean una sola cuenta.

### La sesión (D2, S1)

Tabla nueva `cinema.sessions`:

| Columna | Para qué |
|---|---|
| `id` | identificador opaco que viaja en la cookie |
| `customer_id` | a quién pertenece |
| `created_at`, `last_seen_at` | auditoría mínima |
| `expires_at` | caducidad; una sesión no es eterna |
| `revoked_at` | cierre explícito, sin borrar la fila |

El token va en cookie `HttpOnly`, emitida por **Next** (no por el API, que vive en otro origen), y
cada acción de servidor lo presenta al API como credencial propia. El API no pone cookies.

### El correo de acceso

Mismo `Sender`, mismo estilo de mensaje, **otro texto**: un enlace que caduca en minutos y sirve una
vez. No es el correo de las entradas y no debe parecerse, porque el de las entradas caduca en una
semana y se puede reenviar.

### La promoción (D5, S3)

```
verificar el token ──► guest_customers (email, verified_at=now)   ← crear o encontrar
                   └─► sessions (customer_id, expires_at)
                   └─► reservations SET customer_id WHERE contact_email = email
```

Todo en una transacción, y el reclamo solo toca filas cuya dirección coincide exactamente con la
verificada.

## Trampas y límites que este diseño no resuelve

- **Lo ya entregado no queda protegido.** Un enlace de entrada repartido antes del login sigue
  abriendo esa entrada para siempre. La sesión protege la lista, no los enlaces.
- **Quien controla el buzón, entra.** Es la definición de enlace mágico y también su límite: una
  cuenta de correo compartida es una cuenta compartida.
- **Si el proveedor de correo está caído, no se entra.** Aceptable para un demo, y hay que decirlo:
  la compra no depende del correo, pero el login sí.
- **No hay cambio de dirección ni borrado de cuenta.** Con identidad real llega el RGPD de verdad:
  hoy no hay forma de que una persona pida que se borren sus datos. Es un límite que hay que
  escribir en el README, no esconder.
- **La sesión no se renueva sola.** Caduca y se vuelve a entrar; no hay refresco silencioso.
- **Una entrada reclamada sigue sin dueño operativo**: no hay transferencia, ni reenvío entre
  cuentas, ni reembolso. El login no adelanta el trabajo de pagos.

## Tareas previstas

| # | Tarea | Specs |
|---|---|---|
| T1 | Migración: `guest_customers`, `sessions`, `reservations.customer_id` | S1, S3, S8 |
| T2 | API: emitir enlace de acceso (firmante propio, nonce, caducidad corta) y consumirlo | S2, S3 |
| T3 | API: crear sesión, leerla, revocarla; presentarla en las lecturas de la cuenta | S1, S5 |
| T4 | API: `GET /me/tickets` para la cuenta de la sesión | S5 |
| T5 | API: reclamo de compras anteriores en la verificación | D5, S8 |
| T6 | Web: pedir enlace, consumirlo, cookie de sesión y acción de cerrar | S1, S2 |
| T7 | Web: `/tickets` con sesión lee del servidor; sin sesión dice la verdad | S5 |
| T8 | Web: retirar la sesión de demostración y su etiqueta | S7 |
| T9 | Gates: `verify-identity` (enlace caducado, enlace reusado, límite, revocación, reclamo) | S1–S6 |
| T1b | Gates: ciclos 0009 y 0010 en `verify_migrations.sh`, que se había quedado en 0008 | S8 |

Esfuerzo estimado: dos o tres días, con T1–T5 como primer corte (por API, con `curl`) y T6–T9 como
segundo.

## Slices

- **Slice 1 — el esquema y el enlace firmado**: tres commits, 1.612 líneas.
  - `feat(db): a table for the account, one for the session, one for the link` (194 líneas):
    migración 0010 en ambos sentidos — `guest_customers`, `sessions` (hash, nunca el token),
    `access_links` (nonce de un solo uso) y `reservations.customer_id` nullable con `set null`.
  - `feat(identity): sign a link that proves an address, in minutes` (1.142 líneas, casi todo
    tests): el tercer firmante, `API_IDENTITY_LINK_KEY` y `API_IDENTITY_LINK_TTL` (15m),
    `NormalizeEmail` dentro del firmante y diez comportamientos probados a reloj fijo. Va por
    encima del objetivo de ~400 líneas y es deliberado: un firmante sin sus bordes probados es
    un firmante que no se sabe si firma lo que dice.
  - `test(migrations): make the gate cover 0009 and 0010 instead of stopping at 0008` (276
    líneas): el gate se había quedado en 0008 y su verde describía una base de datos dos
    migraciones por detrás. Las aserciones nuevas se probaron contra copias mutadas de las
    migraciones —un CHECK debilitado, una clave compuesta reducida a una columna, un rollback que
    borra la cola, y las dos acciones de borrado intercambiadas— y cada mutación fue cazada por un
    fallo con nombre.
- **Slice 2 — la sesión** (`feat(identity): a session is a token the client holds and a hash we
  keep`): 1.590 líneas, casi todas tests. `Open` acuña 32 bytes de `crypto/rand` y devuelve el
  token una sola vez; la base guarda su SHA-256, así que un dump no se puede replicar como sesión.
  Caducada, revocada e inexistente son **un solo** error —lo contrario sería un oráculo de «¿existió
  esta credencial?»— y un fallo del almacén es otro distinto, para que una caída nunca se lea como
  un cierre de sesión. `Close` no responde a la pregunta de si el token existía: a un cliente que
  se le dice que su salida falló se le convence de que sigue dentro. La vigencia la decide el SQL
  (`revoked_at IS NULL AND expires_at > now`), no el llamante. Autenticar refresca `last_seen_at` y
  **no** extiende `expires_at`: una sesión que se renueva sola no acaba nunca. `API_SESSION_TTL`
  son 720h por defecto.
  13 comportamientos con un store falso y 6 contra PostgreSQL real, incluidos que la fila caducada
  se rechaza estando presente, que la doble revocación conserva el primer instante y que borrar la
  cuenta arrastra sus sesiones.
- **Slice 3 — emitir y canjear el enlace**: seis archivos nuevos, ~1.800 líneas, casi todas tests.
  `Issue` cuenta por dirección **antes** de nada, falla cerrado si el contador no responde, firma y
  luego **relee la caducidad del propio token** en vez de calcularla dos veces, guarda la fila y solo
  entonces manda el correo — y su error es el mismo tanto si el contador se negó como si el almacén
  falló, para que la respuesta no sea un oráculo. `Redeem` canjea en **una transacción**: gasta el
  nonce, crea la cuenta con `verified_at` (la primera verificación es la que cuenta), reclama las
  órdenes de esa dirección y escribe la sesión — la sesión se escribe ahí y no por `SessionService`
  justamente porque tiene que estar dentro. Las cinco pruebas gated contra PostgreSQL real cubren el
  doble canje (segundo intento: `ErrLinkSpent` y nada escrito), la reclamación selectiva y que un
  segundo enlace no mueve `verified_at`.
- **Slice 4 — las rutas y el cableado**: `POST /api/v1/identity/links` y `POST /api/v1/identity/sessions`
  más el contador en Redis y el montaje en `cmd/api`. El `202` es **el mismo byte a byte** para
  «enviado», «tope agotado» y «contador ilegible»: es lo que hace que la ruta no sirva de oráculo, y
  hay un test que lo compara con `bytes.Equal`. Los tres modos de fallo de un enlace (gastado,
  caducado, falsificado) comparten un `401` con cuerpo fijo. El `201` es el **único** momento en que
  el token de sesión aparece en una respuesta; el API no pone cookies, las pone Next. Límite de 3 por
  dirección y 15 minutos, con `EXPIRE` solo en el primer incremento y Redis caído = error, nunca
  permiso.
- **Slice 5 — `GET /me/tickets`**: la lectura que hace que una cuenta sirva para algo. El token de
  sesión llega en `Authorization: Bearer`; caducado, revocado, malformado e inventado responden el
  **mismo** 401 con el **mismo cuerpo** que la ruta de entrada; una cuenta sin compras responde
  `200 {"tickets":[]}` y no un 404, porque la cuenta existe y simplemente no ha comprado. La lista
  reutiliza **la misma proyección** que la página de la entrada (un test compara las dos respuestas
  byte a byte para el mismo ticket) y se ordena por instante de emisión con el identificador como
  desempate, porque dos entradas de un pedido comparten instante.
  La consulta se acota por `reservations.customer_id` y nada más — ni `contact_email` (dirección
  nunca verificada en 0009, y una lista construida sobre ella filtraría pedidos ajenos por una
  errata) ni el identificador de la entrada, que es capacidad y no propiedad. `customer_id` nulo no
  aparece en la lista de nadie.
  El gated contra PostgreSQL **falló la primera vez** y por una razón que importa: la aserción
  esperaba el orden ascendente mientras la consulta desempata descendente. El fallo era del test, no
  de la consulta, y se corrigió el test — no la consulta.

- **Slice 6 — el reclamo se anota (T5)**: la redención devuelve ahora `Redemption{CustomerID,
  Claimed}` —el número sale del `RowsAffected` de la propia sentencia de reclamo, no de un segundo
  conteo— y el `Redeemer` escribe **una** línea al terminar bien:
  `identity: address verified` con `customer=<id>` y `claimed=<n>`. Ni la dirección, ni el enlace, ni
  el token, ni un hash; una redención rechazada no escribe nada. El recuento **no** viaja al cliente:
  cuántos pedidos reclamó una cuenta no es asunto de quien llama, y el cuerpo del `201` sigue siendo
  exactamente el mismo. Cero reclamados también se registra: una cuenta nueva sin historial es un
  dato, y una rama que solo loguea cuando `n > 0` es una rama que nadie prueba.

## Revisión nativa

| PR | Lentes | Resultado |
|---|---|---|
| #111 | risk, resilience | aprobada, autoridad quemada |
| #112 | risk, resilience | aprobada, autoridad quemada |
| #113 | — (documentación) | sin revisión: no hay cambio ejecutable |
| #114 | risk, resilience | aprobada, autoridad quemada |
| #115 | risk, resilience | aprobada, autoridad quemada |
| #116 | risk, resilience | **risk admitido; resilience nunca admitido** |

En #116 el modelo del lente de resiliencia emite un payload con un campo `evidence` en la raíz **y**
dentro de cada hallazgo, que no está en el esquema del admittedor: `json: unknown field "evidence"`,
rechazado dos veces y con el segundo intento devolviendo los mismos bytes en 143 ms. Se intentó lo
que manda el proveedor —`STATUS` fresco y sólo ese slot— y el `start` posterior respondió
`"action": "replayed"`: no había nada nuevo que ejecutar. La autoridad quedó **sin quemar** y el
payload rechazado conservado por el proveedor.

Decisión del responsable del producto: **fusionar** con la carencia escrita, como en el gate del
email, y **abandonar el linaje** después. La revisión local fue completa —`go test ./apps/api/...`
verde, el gated contra PostgreSQL real pasado, y el flujo probado a mano de punta a punta— así que lo
que falta es una vista externa, no una comprobación.

## Log

- **L1** — Petición del usuario, literal: «estaba login y pago, con qué continuamos?» Elegido en la
  pregunta de continuidad: **estudiar identidad y login** antes que pagos reales.
- **L2** — Decisiones del usuario, las cuatro: **identifica el enlace mágico por correo**; **entrar
  gana que «mis entradas» pase a ser una lectura del servidor**; **la compra de invitado sigue sin
  cuenta**; y **al verificar se reclaman las compras anteriores hechas con esa dirección**. La
  quinta (`D2`) se cerró en el estudio como decisión de ingeniería: token opaco en base de datos,
  porque una sesión sin estado no se puede revocar.
