# Propuesta — Identidad visual e icono de aplicación de Umbral

> **Estado:** en revisión
> **Specs base afectadas:** `specs/prd/umbral-mvp.md`, `specs/technical/umbral-architecture.md`, `specs/plans/umbral-mvp-plan.md`
> **Fecha:** 2026-09-11 · **Autor:** Ernesto Crespo (borrador asistido)
> **Artefacto de entrada:** `assets/branding/umbral-icons/` (kit v0.1)

**Problema/motivación.** Las specs base no definen cómo se identifica la aplicación en los
escritorios. Faltan:

- el nombre del icono y el app-id;
- el archivo `.desktop`;
- los formatos `.ico` / `.icns` / `.icon`.

Sin esto, el paquete `.deb` de la release 0.1 y el cliente de escritorio de F2 mostrarían un
icono genérico. En Wayland, GNOME además no asocia la ventana a su lanzador si el `app_id`
no coincide con el nombre del `.desktop`. Ya existe un kit de iconos generado (concepto
"puerta · luz · prompt · umbral") que hay que incorporar como requisito verificable.

**Alcance.**

- Qué cambia:
  - fija el app-id `io.github.ecrespo.Umbral`;
  - añade requisitos de empaquetado (REQ-PKG-*);
  - añade una sección de identidad visual al Tech Design;
  - añade tareas de empaquetado al hardening de 0.1 y a F2.
- Qué **no** cambia: la API, el modelo de datos y el comportamiento del agente o del terminal.

**Impacto.**

- Sin impacto en el contrato JSON-RPC ni en migraciones.
- La release 0.1 (solo TUI) instala iconos y un `.desktop` con `Terminal=true`.
- En F2, el cliente Wails usa el mismo app-id como `app_id`/`WM_CLASS` y los formatos de Windows y macOS.
- **Riesgo:** el Taskfile por defecto de Wails v3 regenera `.ico` / `.icns` desde un único PNG y pisaría la baldosa de macOS. Se mitiga con REQ-PKG-006.

**Constitution check.**

- **Art. 2:** cada REQ-PKG tiene tarea y verificación automatizable (checksums, `desktop-file-validate`, inspección del `.ico` / `.icns`).
- **Art. 9:** entra como Delta porque modifica specs ya propuestas.
- **Art. 4:** no introduce dependencias de red.
- Sin excepciones.

**Decisión abierta.** App-id alternativo `to.seraph.Umbral` (dominio propio). Se elige
`io.github.ecrespo.Umbral` porque se verifica con la cuenta de GitHub, que es lo que pide
Flathub. Cambiarlo más adelante implica regenerar el kit con `tools/build.py --app-id`.
