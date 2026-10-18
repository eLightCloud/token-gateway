const env = process.env
const user = encodeURIComponent(env.POSTGRES_USER || 'root')
const password = encodeURIComponent(env.POSTGRES_PASSWORD || '123456')
const database = encodeURIComponent(env.POSTGRES_DB || 'new-api')
const port = env.POSTGRES_PORT || '54329'

process.stdout.write(
  env.SQL_DSN ||
    `postgresql://${user}:${password}@127.0.0.1:${port}/${database}?sslmode=disable`
)
