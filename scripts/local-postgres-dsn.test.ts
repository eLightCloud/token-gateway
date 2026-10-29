import { expect, test } from 'bun:test'
import { localDatabaseDsn } from './local-postgres-dsn'

test('keeps explicit SQLite mode instead of silently connecting to PostgreSQL', () => {
  expect(localDatabaseDsn({ SQL_DSN: '', POSTGRES_PORT: '54329' })).toBe('')
})
test('keeps a configured database and builds an escaped PostgreSQL default only when absent', () => {
  expect(localDatabaseDsn({ SQL_DSN: 'postgresql://synthetic/db' })).toBe(
    'postgresql://synthetic/db'
  )
  expect(
    localDatabaseDsn({
      POSTGRES_USER: 'test@user',
      POSTGRES_PASSWORD: 'test/pass',
      POSTGRES_DB: 'test db',
      POSTGRES_PORT: '5544',
    })
  ).toBe('postgresql://test%40user:test%2Fpass@127.0.0.1:5544/test%20db?sslmode=disable')
})
