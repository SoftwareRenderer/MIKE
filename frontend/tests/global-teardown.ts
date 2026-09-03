// Playwright calls the DEFAULT export of whatever globalTeardown names, so naming global-setup.ts
// here would re-run setup at the end of every run — spawning a server pair, lock and database that
// nothing would ever stop, and the next run would boot on a used database.
export { teardown as default } from './global-setup';
