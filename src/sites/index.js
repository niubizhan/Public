import fofoys from './fofoys.js';
import piaoys from './piaoys.js';
import xcyycn from './xcyycn.js';

export const SITES = [fofoys, piaoys, xcyycn];

export function getSite(key) {
  return SITES.find((s) => s.key === key) || null;
}
