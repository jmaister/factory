# Software factory con Vikunja

## Objetivo

Vikunja es la interfaz de trabajo: creas una tarea, un agente la analiza, diseña, implementa y testea, y tú apruebas o pides cambios moviendo la tarjeta. El código vive en GitHub; Forgejo solo hace CI y deploy.

## Estados (buckets del kanban)

| Estado | Quién actúa | Significado |
|---|---|---|
| DRAFT | Tú | Especificación en escritura. El orquestador la ignora. |
| TODO | Orquestador | Lista para el agente. Mover aquí = “la especificación está lista”. |
| EN CURSO | Agente | Analizando, diseñando, implementando o testeando. |
| REVISIÓN | Tú | PR abierto en GitHub y CI verde. Esperando tu decisión. |
| REWORK | Agente | Tú has pedido cambios; el agente retoma la misma rama. |
| APROBADO | Orquestador | Mergea el PR y crea el tag que dispara el deploy. |
| HECHO | — | Mergeado, tag creado, deploy lanzado. |

Transiciones:

- `DRAFT → TODO`: tú.
- `TODO | REWORK → EN CURSO`: orquestador.
- `EN CURSO → DRAFT`: agente, cuando la especificación no es clara. Deja un comentario con las preguntas concretas. Tú respondes en la tarjeta y la pasas de nuevo a `TODO`.
- `EN CURSO → REVISIÓN`: agente, cuando el PR existe y el CI está verde.
- `EN CURSO → REWORK`: agente, si el CI falla tras N intentos internos (ver límites).
- `REVISIÓN → REWORK`: tú, con comentarios en la tarjeta.
- `REVISIÓN → APROBADO`: tú.
- `APROBADO → HECHO`: orquestador, tras merge y tag.

## Flujo técnico

1. **Código**: GitHub es la fuente de verdad. Forgejo tiene un **mirror** del repo que se sincroniza desde GitHub.
2. **Agente**: clona desde GitHub en un contenedor aislado, crea `task/<id>-<slug>`, hace push a GitHub y abre el PR.
3. **CI**: el push llega al mirror de Forgejo, que ejecuta el CI (`on: push`). Los jobs publican su estado de commit en GitHub, como ya hace `taronja-gateway/.forgejo/workflows/ci.yml`. El orquestador lee los checks del PR en GitHub, no habla con Forgejo.
4. **Aprobación**: al pasar a `APROBADO`, el orquestador:
   - hace merge del PR en GitHub (API), con el token del orquestador;
   - crea un tag en `main` vía API de GitHub (`vX.Y.Z`);
   - el mirror sincroniza el tag y el workflow de deploy de Forgejo se dispara por `tags: v*`.
5. **Vuelta**: el orquestador comenta en la tarjeta el resultado (tag creado, enlace al run de deploy) y la pasa a `HECHO`.

Puntos a comprobar en la implementación:

- **Latencia del mirror**: si la sincronización es periódica, el CI tarda en arrancar. Se puede forzar la sync vía API de Forgejo tras cada push del agente.
- **Sincronización de tags**: confirmar que el mirror trae tags nuevos y que el workflow de deploy escucha `tags: v*`.
- **Versionado**: por defecto, bump de patch en cada aprobación. Se puede cambiar por repo.

## Orquestador

- Servicio Python en un contenedor del Proxmox (systemd o docker compose).
- Polling de Vikunja cada ~60 s (los webhooks quedan para una fase posterior).
- Un worker por tarea, con bloqueo mediante etiqueta `agente:trabajando` para evitar dobles ejecuciones.
- Estado persistido: el estado real está en Vikunja y en GitHub; el orquestador debe poder reconstruirlo desde ahí tras un reinicio.

## Agente

- Claude Code en modo headless (`claude -p`) en un contenedor por tarea, sin credenciales de producción.
- Entrada: título, descripción y comentarios de la tarjeta, más el `CLAUDE.md` del repo.
- Salida: rama, PR y comentario en la tarjeta con plan, cambios y resultado de tests.
- Ciclo: analizar → diseñar (plan en comentario) → implementar con tests → push → esperar CI → `REVISIÓN`.

## Tokens y permisos

| Actor | GitHub | Forgejo | Vikunja |
|---|---|---|---|
| Agente | Contents y PR en el repo; sin merge, sin push a `main` | — | Solo comentarios |
| Orquestador | Merge, tags, lectura de checks | Sync del mirror (si se usa) | Mover tarjetas, comentar |
| Tú | Revisión del PR (opcional) | — | Mover tarjetas |

- **Branch protection en `main`**: PR obligatorio y checks del CI obligatorios.
- Tokens con alcance mínimo, guardados fuera del repo.

## Límites y seguridad

- Máximo 3 rondas de `REWORK` automáticas; después la tarjeta pasa a `DRAFT` con resumen de lo intentado y pide decisión humana.
- Un agente por tarea, con timeout configurable.
- El agente nunca mergea ni despliega; esas acciones solo las hace el orquestador tras `APROBADO`.

## Pendiente

- Repo piloto.
- URL de la instancia de Vikunja y token de API.
- Token de GitHub (fine-grained, con alcance al repo piloto).
- Confirmar que existe un workflow de deploy en Forgejo que escuche tags, o crearlo.
