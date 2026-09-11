# Tasks — Delta 2026-09-identidad-icono

> Delta: `changes/2026-09-identidad-icono/delta-spec.md` · Generado: 2026-09-11

## Tareas

### [ ] T-PKG-01 · Incorporar el kit y fijar checksums
- **Qué:**
  - copiar el kit a `assets/branding/umbral-icons/`;
  - generar `CHECKSUMS.sha256` de todos los binarios;
  - crear el job de CI `icons` (regenerar con `tools/build.py` y comparar).
- **REQ:** REQ-PKG-001, REQ-PKG-006
- **Depende de:** —
- **Done:** job `icons` en verde; modificar un píxel de un PNG pone el job en rojo.

### [ ] T-PKG-02 · `.desktop` de la release 0.1
- **Qué:** ajustar el `.desktop` a `Exec=umbral-tui` y `Terminal=true`, y validarlo en CI.
- **REQ:** REQ-PKG-002, REQ-PKG-003
- **Depende de:** T-PKG-01
- **Done:** `desktop-file-validate share/applications/io.github.ecrespo.Umbral.desktop` sin errores.

### [ ] T-PKG-03 · Instalación en el `.deb`
- **Qué:** incluir el árbol hicolor y el `.desktop` en el paquete; los hooks postinst/trigger actualizan las cachés (`gtk-update-icon-cache`, `update-desktop-database`).
- **REQ:** REQ-PKG-001, REQ-PKG-002
- **Depende de:** T-PKG-02
- **Done:** `dpkg -c umbral_*.deb` lista los 11 archivos de icono y el `.desktop`; test de humo en un contenedor Ubuntu 26.04.

### [ ] T-PKG-04 · Wails v3: iconos, app_id y override del Taskfile (F2)
- **Qué:**
  - sustituir el task `generate:icons` para que copie `wails-build/` en lugar de regenerar;
  - fijar el app-id de la aplicación y de la ventana;
  - incrustar el `.ico`.
- **REQ:** REQ-PKG-004, REQ-PKG-005, REQ-PKG-006
- **Depende de:** T-PKG-01, cliente de escritorio de F2
- **Done:**
  - en GNOME Wayland, el dock muestra el icono del kit para la ventana abierta (verificación manual con captura en `docs/qa/`);
  - la inspección del `.exe` lista las 8 entradas del `.ico`.

### [ ] T-PKG-05 · macOS Tahoe: `.icon` y `Assets.car` (F2)
- **Qué:** ensamblar `appicon.icon` en Icon Composer desde las capas; compilar con `wails3 generate icons -iconcomposerinput`; validar los modos oscuro/claro/tintado.
- **REQ:** REQ-PKG-007
- **Depende de:** T-PKG-04
- **Done:** `Info.plist` con `CFBundleIconName = appicon`; en macOS 26 el Dock no muestra la baldosa genérica (captura en `docs/qa/`).

## Matriz de trazabilidad

| REQ | Tareas | Verificación |
|---|---|---|
| REQ-PKG-001 | T-PKG-01, T-PKG-03 | job `icons`, `dpkg -c` |
| REQ-PKG-002 | T-PKG-02, T-PKG-03 | `desktop-file-validate` |
| REQ-PKG-003 | T-PKG-02 | revisión del `.desktop` en CI |
| REQ-PKG-004 | T-PKG-04 | QA manual GNOME Wayland |
| REQ-PKG-005 | T-PKG-04 | inspección del `.ico` incrustado |
| REQ-PKG-006 | T-PKG-01, T-PKG-04 | checksums en CI |
| REQ-PKG-007 | T-PKG-05 | QA manual macOS 26 |
| REQ-PKG-008 | — | SHOULD, diferido |
