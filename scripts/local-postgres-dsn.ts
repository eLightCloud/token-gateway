export function localDatabaseDsn(env: Record<string, string | undefined>): string {
  // An explicitly empty SQL_DSN selects the Go server's existing SQLite mode.
  if (env.SQL_DSN !== undefined) return env.SQL_DSN
  const user = encodeURIComponent(env.POSTGRES_USER || 'root')
  const password = encodeURIComponent(env.POSTGRES_PASSWORD || '123456')
  const database = encodeURIComponent(env.POSTGRES_DB || 'new-api')
  const port = env.POSTGRES_PORT || '54329'
  return `postgresql://${user}:${password}@127.0.0.1:${port}/${database}?sslmode=disable`
}

if (import.meta.main) process.stdout.write(localDatabaseDsn(process.env))
