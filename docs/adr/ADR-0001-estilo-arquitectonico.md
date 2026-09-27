# ADR-0001: Estilo arquitectónico de Umbral

- **Estado**: Propuesto
- **Fecha**: 2026-09-11
- **Decisores**: Ernesto Crespo (Tech Lead)
- **Sistema / alcance**: Umbral completo (daemon, clientes, CLI)

## Contexto
Inputs de decisión:
- **Organización:** una persona más agentes de código; sin equipo de operaciones.
- **Dominio:** terminal + runtime agéntico. Hay dos subdominios ricos (terminal/bloques y agente/políticas); no aplica regulación.
- **Escalabilidad:** una sola máquina. El cuello de botella es la latencia de E/S del PTY y la de los modelos, no el throughput.
- **Consistencia:** local y ACID (SQLite). El historial del agente es auditable (Art. 5 y 7), sin requisito de event sourcing.
- **Evolución:** alta tasa de cambio; extensibilidad por terceros vía MCP, ACP y proveedores de modelos.
- **Operación:** el blast radius es la máquina del usuario. Cerrar la UI no puede matar shells ni agentes.
- **Supuestos:** sin backend en la nube propio; distribución como binarios por plataforma.

## Decisión
**Base**: cliente-servidor local, con el daemon `umbrald` como **monolito modular** ·
**Interior por unidad**: hexagonal (domain / ports / adapters) · **Complementos**:
- microkernel para herramientas, proveedores y plugins MCP/WASM;
- bus de eventos en proceso;
- estilo agentic en la capa de agentes;
- pipes & filters en el motor de contexto.

Diagrama: `docs/ARQUITECTURA-UMBRAL.md` §5.1 y `specs/technical/umbral-architecture.md` §3.1.

Reglas de frontera que se harán cumplir: `.go-arch-lint.yml` según el Tech Design §5.2
(Constitución Art. 3). Tarea T-F0-01.

## Alternativas consideradas

| Alternativa | Atributos a favor | Atributos en contra | Por qué no |
|---|---|---|---|
| App monolítica sin daemon (la UI es dueña de los PTY) | simplicidad | cerrar la UI mata shells y agentes; un solo cliente | contradice resiliencia y multi-cliente |
| Microservicios locales (un proceso por módulo) | aislamiento de fallos | IPC, despliegue y depuración complejos | ningún input exige despliegue independiente |
| Electron + Node (estilo Wave) | ecosistema web | memoria, runtime extra | Go + WebView (Wails v3) cubre lo mismo más ligero |

## Consecuencias
**Positivas:**
- clientes intercambiables;
- sesiones durables;
- base para agentes en background;
- protocolos estándar.

**Negativas / costos asumidos:**
- un protocolo cliente-daemon que versionar;
- cgo por libghostty.

**Anti-patrones a vigilar y mitigación:**

| Anti-patrón | Mitigación |
|---|---|
| God module en `agents` | puertos estrechos + `go-arch-lint` |
| Eventos sin contrato | tipos de evento versionados en `internal/bus` |
| Fallback en cascada con meta-proveedores | sin fallback propio dentro de OpenRouter / OmniRoute |

## Plan de adopción
1. F0: daemon + TUI.
2. F1: agente.
3. F2: cliente de escritorio sobre el mismo protocolo.

**Métricas de éxito:**
- latencia añadida < 5 ms p95;
- 0 violaciones de frontera en CI;
- US-003 offline ≥ 70 %.

## Revisar cuando
Aparezca colaboración en vivo multiusuario o ejecución de agentes en infraestructura
compartida. Ahí surge un servicio remoto propio.

```
Recomendación: Cliente-servidor local + Monolito modular + Hexagonal (+ Microkernel, bus de eventos, Agentic, Pipes & Filters en contexto)
Por qué: resiliencia de sesiones y agentes, extensibilidad vía MCP/ACP/proveedores, equipo pequeño con binario único
No elegí app sin daemon porque: la UI no puede ser dueña de shells ni de agentes en background
No elegí microservicios porque: no hay equipos ni escalado independientes que lo justifiquen
Riesgos / anti-patrones a vigilar: god module en agents, API inestable de libghostty, tool calling local, fallback en cascada
Revisar cuando: aparezca colaboración en vivo multiusuario o ejecución remota compartida
```
