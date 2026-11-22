# Permisos de los tokens

Todos los tokens viven en `academia/factory.env` (permisos 600, excluido de git). El YAML solo los referencia como `${VAR}`.

| Variable | Servicio | Para qué | Permisos mínimos |
|---|---|---|---|
| `VIKUNJA_TOKEN` | Vikunja (tickets) | Leer el tablero, crear tareas, mover entre columnas, comentar, webhooks | Ver abajo |
| `GITHUB_TOKEN` | GitHub (código) | Leer el repo, crear ramas, abrir PRs, leer su estado, crear tags, hacer merge | Ver abajo |
| `FORGEJO_TOKEN` | Forgejo (CI) | Leer los runs de Actions de cada commit | Ver abajo |
| `FACTORY_WEBHOOK_TOKEN` | Factory (webhook) | Autenticar los avisos de Vikunja hacia la factory | No necesita permisos: es un secreto propio que va en la URL del webhook |

## VIKUNJA_TOKEN

Se crea en Vikunja: Settings → API Tokens. Los permisos se agrupan por recurso. Lo que usa la factory:

- **Projects**: leer (buscar el proyecto por nombre, listar proyectos).
- **Views**: leer (encontrar la vista kanban del proyecto).
- **Buckets**: leer y escribir (crear/renombrar columnas y mover tareas entre ellas).
- **Tasks**: leer, crear y actualizar (crear tareas, leer su descripción, actualizar su título y sus campos al mover).
- **Comments**: leer y crear (la factory deja comentarios con sesión, PR, CI y errores).
- **Webhooks**: leer y crear (solo para `factory webhook install`).

Nota: si el token no tiene el grupo de **Buckets**, `GET` a las columnas devuelve 401 aunque `/projects` funcione. Ese fue el síntoma que nos costó detectar.

## GITHUB_TOKEN (fine-grained, solo el repo `jmaister/academia`)

Se crea en GitHub: Settings → Developer settings → Fine-grained tokens. Limitar el acceso a **un único repo**.

| Permiso | Nivel | Para qué |
|---|---|---|
| Metadata | Read | Obligatorio por GitHub: info básica del repo |
| Contents | **Read and write** | Hacer push de las ramas de las tareas y crear tags (`git/refs`) |
| Pull requests | **Read and write** | Crear el PR, leer su estado y **hacer merge** (`pulls/{n}/merge`) |
| Commit statuses | Read | (opcional) estados de commit |
| Checks | Read | (opcional) checks de GitHub Actions |
| Actions | Read | (opcional) número de runs de GitHub Actions |

Puntos a comprobar:

- **Pull requests: Write** es necesario para que la factory haga el merge cuando se mueve una tarea a 👍 APROBADO. Hasta ahora no se ha verificado con este token, porque no hemos hecho ningún merge automático todavía.
- Sin **Commit statuses** y **Checks** no se pueden leer los checks de commit; por eso la factory usa `mergeable_state` del PR para el estado de GitHub, y los runs de Forgejo para el CI real.
- Si el repo tiene **branch protection** que exige revisión o checks, el merge automático puede fallar: la factory lo deja como comentario en la tarea.

## FORGEJO_TOKEN

Se crea en Forgejo: Settings → Applications → Generate token. Usuario: el que tenga acceso a `Factorizable/academia`.

| Scope | Para qué |
|---|---|
| `read:repository` | Leer los runs de Actions (`/repos/{owner}/{repo}/actions/runs`) |
| `read:user` | (opcional) comprobar el usuario con `/user` |

Probado: el token actual puede leer los runs del repo, incluidos los de cada commit. No hace falta ningún permiso de escritura: la factory no ejecuta workflows ni cambia nada en Forgejo.

## Comprobaciones rápidas

```bash
# GitHub: ver los permisos del token sobre el repo
curl -s -H "Authorization: Bearer $GITHUB_TOKEN" https://api.github.com/repos/jmaister/academia | jq .permissions

# Forgejo: listar runs
curl -s -H "Authorization: token $FORGEJO_TOKEN" \
  "http://192.168.1.35:13000/api/v1/repos/Factorizable/academia/actions/runs?limit=3"

# Vikunja: columnas del tablero (si da 401, falta el grupo Buckets)
curl -s -H "Authorization: Bearer $VIKUNJA_TOKEN" http://192.168.1.35:13456/api/v1/projects/5/views/24/buckets
```

Si algo falla con 403 o 401, el mensaje indica qué recurso no se puede leer o escribir.
