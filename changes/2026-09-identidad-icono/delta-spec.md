# Delta — Identidad visual e icono de aplicación

Los REQ nuevos toman IDs nuevos (`REQ-PKG-*`). No se elimina ningún REQ existente.

## ADDED

### specs/prd/umbral-mvp.md → §6.10 Empaquetado e identidad (PKG)

- **REQ-PKG-001** · MUST · ubicuo — EL SISTEMA DEBERÁ instalar su icono en el tema `hicolor` con el nombre `io.github.ecrespo.Umbral`, en `scalable/apps` (SVG) y en PNG de 16, 22, 24, 32, 48, 64, 128, 256 y 512 px, además de `symbolic/apps/io.github.ecrespo.Umbral-symbolic.svg`.
- **REQ-PKG-002** · MUST · ubicuo — EL SISTEMA DEBERÁ instalar `io.github.ecrespo.Umbral.desktop` con `Icon=io.github.ecrespo.Umbral` y `StartupWMClass=io.github.ecrespo.Umbral`, y ese archivo DEBERÁ pasar `desktop-file-validate` sin errores.
- **REQ-PKG-003** · MUST · opcional — DONDE se distribuya solo la TUI (release 0.1), el `.desktop` DEBERÁ usar `Exec=umbral-tui` con `Terminal=true`.
- **REQ-PKG-004** · MUST · evento — CUANDO el cliente de escritorio (F2) cree su ventana, EL SISTEMA DEBERÁ fijar `app_id` en Wayland y `WM_CLASS` en X11 a `io.github.ecrespo.Umbral`.
- **REQ-PKG-005** · MUST · ubicuo — EL SISTEMA DEBERÁ incrustar en el ejecutable de Windows (F2) un `.ico` con entradas de 16, 20, 24, 32, 40, 48, 64 y 256 px, donde las de 16 y 24 provienen de las fuentes dibujadas a mano.
- **REQ-PKG-006** · MUST · no deseado — SI el pipeline de build de Wails intenta regenerar `icon.ico` o `icons.icns` desde `appicon.png`, ENTONCES el build DEBERÁ usar los archivos del kit y fallar si sus SHA-256 difieren de `assets/branding/umbral-icons/CHECKSUMS.sha256`.
- **REQ-PKG-007** · MUST · ubicuo — EL SISTEMA DEBERÁ, en macOS (F2), incluir `Assets.car` compilado desde `appicon.icon` (Icon Composer) con `CFBundleIconName = appicon`, y un `icons.icns` de respaldo para macOS ≤ 15.
- **REQ-PKG-008** · SHOULD · ubicuo — EL SISTEMA DEBERÍA proveer una variante del icono para builds nightly, distinguible a 48 px.

### specs/technical/umbral-architecture.md → §9.3 Identidad visual y empaquetado (nuevo)

- **Fuente única:**
  - `assets/branding/umbral-icons/tools/svgs.py` genera todos los formatos (`tools/build.py`);
  - los tamaños de 16 y 24 px tienen fuentes propias;
  - del 32 en adelante se usa el maestro de 128.
- **Linux:** árbol `share/icons/hicolor` más el `.desktop`; la variante Breeze es opcional y no se instala por defecto.
- **Windows:** `umbral.ico` + `Assets/` MSIX (`Square44x44Logo` con escalas y `targetsize-*_altform-unplated`).
- **macOS:**
  - las capas en `macos/icon-composer-layers/` se ensamblan en Icon Composer → `build/appicon.icon`;
  - Wails compila con `actool` (Xcode 26);
  - `umbral.icns` sirve de respaldo.
- **CI:** job `icons` que regenera el kit, compara los checksums y valida el `.desktop`.

### specs/plans/umbral-mvp-plan.md → Hardening 0.1 y F2

- Hardening 0.1: añadir T-PKG-01 … T-PKG-03.
- F2: añadir T-PKG-04 y T-PKG-05.

## MODIFIED

### assets/branding/umbral-icons/linux/share/applications/io.github.ecrespo.Umbral.desktop
- **Antes:** `Exec=umbral-desktop %U`, `Terminal=false`.
- **Después (0.1):** `Exec=umbral-tui`, `Terminal=true` (REQ-PKG-003). En F2 vuelve a `umbral-desktop` mediante un nuevo delta.
- **Razón:** en 0.1 no existe cliente de escritorio.

## REMOVED

— (ninguno)
