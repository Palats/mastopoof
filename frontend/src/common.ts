import { unsafeCSS } from 'lit'

import { Backend } from "./backend";

import normalizeCSSstr from "./normalize.css?inline";
import baseCSSstr from "./base.css?inline";

import dayjs from 'dayjs';
import relativeTimePlugin from 'dayjs/plugin/relativeTime';
dayjs.extend(relativeTimePlugin);
import utcPlugin from 'dayjs/plugin/utc';
dayjs.extend(utcPlugin);
import timezonePlugin from 'dayjs/plugin/timezone';
dayjs.extend(timezonePlugin);

// TODO: use context https://lit.dev/docs/data/context/
export const displayTimezone = dayjs.tz.guess();
console.log("Display timezone:", displayTimezone);

// Create a global backend access.
// TODO: use context https://lit.dev/docs/data/context/
export const backend = new Backend();

export const sharedCSS = [unsafeCSS(normalizeCSSstr), unsafeCSS(baseCSSstr)];