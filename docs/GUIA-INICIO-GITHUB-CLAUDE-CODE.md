# Guía de inicio: repositorio en GitHub + desarrollo con Claude Code

Pasos para pasar de este paquete a un repositorio vivo, con issues por tarea y Claude Code
trabajando tarea a tarea bajo Spec-Driven Development.

## 0. Requisitos en tu máquina

| Herramienta | Para qué | Comprobación |
|---|---|---|
| git + GitHub CLI (`gh`) autenticado | repo, issues, PRs | `gh auth status` |
| Python ≥ 3.10 | `tools/sdd_check.py`, bootstrap, hooks | `python3 --version` |
| Node ≥ 20 | validador Mermaid | `node --version` |
| Go ≥ 1.25 | desde T-F0-01 | `go version` |
| Zig (versión que pida libghostty) | compilar libghostty-vt (T-F0-01) | `zig version` |
| bash, zsh, fish | tests de shell integration (T-F0-08) | `which zsh fish` |
| Claude Code | desarrollo asistido | `claude --version` |
| Ollama con `gpt-oss:20b` | F1 (modelos locales) | `ollama list` |

Instalación de Claude Code: <https://docs.claude.com/en/docs/claude-code/overview>.

## 1. Inicializar el repositorio local

```bash
unzip umbral-repo.zip && cd umbral
git init -b main
npm install --prefix tools --no-audit --no-fund
python3 tools/sdd_check.py        # esperado: 1 CRÍTICO (A-01); lo corrige el primer PR
node tools/mermaid_check.mjs      # esperado: todos válidos
git add -A
git commit -m "docs: initial Umbral specification package (SDD)"
```

`python3 tools/sdd_check.py` sale con código 1 mientras A-01 esté abierto. Por eso el job **Specs**
del CI saldrá en rojo en el primer push. Es intencionado: el primer PR lo pone en verde.

## 2. Crear el repo en GitHub y poblarlo

Primero, en seco, para revisar los comandos:

```bash
python3 scripts/github_bootstrap.py --repo ecrespo/umbral --dry-run --create-repo --protect-main | less
```

Después, de verdad:

```bash
python3 scripts/github_bootstrap.py --repo ecrespo/umbral --create-repo --protect-main
```

El script es idempotente: puedes volver a ejecutarlo sin duplicar nada. Hace lo siguiente:

- crea el repo público y hace push de `main` (añade `--private` si lo prefieres);
- configura descripción, topics, discussions y *squash merge* únicamente;
- activa el *Private Vulnerability Reporting*;
- crea 33 labels (`type:*`, `phase:*`, `area:*`, `severity:*`, `parallel`, `claude-ready`);
- crea 5 milestones: Spec fixes → F0 → F1 → Hardening 0.1 → F2;
- crea **45 issues de tarea**, uno por cada `T-…` pendiente, con sus dependencias enlazadas como `#número`;
- crea **5 epics** con checklists;
- crea **8 issues** de hallazgos del Analyze no cubiertos por un delta (A-08…A-15);
- protege `main`: exige los 4 checks del CI, historial lineal y prohíbe force-push. No exige reviews, porque eres mantenedor único y no puedes aprobar tus propios PR.

## 3. Conectar Claude Code con GitHub (para `@claude` en issues y PRs)

**Opción recomendada:**

1. Abre `claude` dentro del repo.
2. Ejecuta `/install-github-app`. Instala la GitHub App y guarda el secreto.
3. Si propone su propio `claude.yml`, quédate con uno solo: el del repo ya está configurado.

**Opción manual:**

1. Instala la app en <https://github.com/apps/claude> solo para este repo.
2. Ejecuta `claude setup-token` y guarda el token como secreto `CLAUDE_CODE_OAUTH_TOKEN` (Settings → Secrets → Actions).
3. Si prefieres facturación por API, guarda `ANTHROPIC_API_KEY` y descomenta esa línea en `.github/workflows/claude.yml` y `claude-review.yml`.

A partir de ahí tienes dos workflows:

- **Claude Code**: responde a `@claude` en issues, comentarios y reviews.
- **Claude spec review**: revisa automáticamente cada PR contra la constitución y las specs.

## 4. Primera sesión local: cerrar los hallazgos del Analyze

```bash
cd umbral && claude
```

1. Acepta la confianza del directorio. Sin ella no se aplican las reglas `allow` del `.claude/settings.json`.
2. Ejecuta `/status` y comprueba que *Setting sources* incluye *Project settings*. El hook de inicio te mostrará el CRÍTICO A-01 y las próximas tareas.
3. Aprueba el servidor MCP `context7` cuando lo pida.
4. Entra en **Plan Mode** (Shift+Tab hasta ver *plan mode*) y pega:

> Lee AGENTS.md y `changes/2026-09-analyze-fixes/`. Revisa conmigo la propuesta y, si la
> apruebo, ejecuta T-FIX-01 a T-FIX-04 en la rama `spec/analyze-fixes`: pliega el delta en
> `specs/`, actualiza `tools/sdd_check.py` y deja `python3 tools/sdd_check.py` sin CRÍTICOS.
> Después ejecuta `/sdd-analyze`, archiva el delta en `changes/_archivo/` y prepara el commit.

5. Revisa los cambios.
6. Pasa el revisor de solo lectura:
   > usa el subagente spec-guardian sobre esta rama
7. Haz push y abre el PR:
   ```bash
   git push -u origin spec/analyze-fixes
   gh pr create --fill --base main
   ```
8. Con el CI en verde, *squash & merge*. `main` ya está limpio.

## 5. Arrancar F0 con la primera tanda supervisada

Una tarea por sesión o por rama:

```text
/implement-task T-F0-01
```

T-F0-01 crea `go.mod`, el Taskfile, gofumpt/golangci-lint/go-arch-lint y los añade al CI. Desde ese
momento `task lint && task arch && task test` es el gate local.

Continúa con **T-F0-02, T-F0-03 y T-F0-04**: son la primera tanda, de 3-5 tareas como máximo antes de
una revisión humana general. Cierra la tanda así:

```text
/checkpoint f0-tanda-1
```

Revisa el checkpoint y ajusta specs con `/spec-delta` si algo no encajó. Luego escala.

### Trabajo en paralelo (tareas `[P]`)

Las tareas marcadas `[P]` (por ejemplo T-F0-04 y T-F0-08) no comparten archivos. Úsalas con worktrees:

```bash
git worktree add ../umbral-T-F0-08 -b feat/T-F0-08-shell-bootstrap
cd ../umbral-T-F0-08 && claude     # en otra terminal: /implement-task T-F0-08
```

### Tareas pequeñas desde GitHub

En el issue de una tarea `claude-ready`, comenta:

> @claude implementa T-F0-11 siguiendo AGENTS.md y .claude/skills/implement-task/SKILL.md; abre un PR contra main

El workflow crea la rama y el PR. Tú revisas como cualquier otro PR.

## 6. Cadencia

| Cuándo | Qué |
|---|---|
| Cada tarea | `/implement-task` → `spec-guardian` → PR con plantilla → merge |
| Cada 3-5 tareas | `/checkpoint <slug>` |
| Spec equivocada | parar → `/spec-delta <slug> "<motivo>"` → aprobar → plegar en el PR que lo implementa |
| Fin de fase | `/sdd-analyze` + `/checkpoint fase-N`; cerrar el epic y el milestone |
| Antes de F1 | aprobar y plegar el delta de identidad visual (respetando A-12) |

## 7. Configuración personal (no se commitea)

Pon tus preferencias en `.claude/settings.local.json` (modelo, permisos extra). Claude Code lo
excluye de git automáticamente cuando lo crea él. Si lo creas a mano, ya está en `.gitignore`.

## 8. Resolución de problemas

| Síntoma | Causa probable | Qué hacer |
|---|---|---|
| El job *Specs* está en rojo | A-01 u otro CRÍTICO abierto | sección 4 |
| Claude pide permiso para todo | el directorio no es de confianza, o el settings no cargó | `/status`, `/permissions` |
| El hook de inicio no aparece | falta `python3` en el PATH | `/hooks` para inspeccionar |
| `@claude` no responde | falta el secreto o la GitHub App | sección 3; revisa la pestaña Actions |
| El job *Icon kit integrity* avisa en "Rebuild" | otra versión de cairo en el runner | es informativo; solo el `sha256sum -c` bloquea |
