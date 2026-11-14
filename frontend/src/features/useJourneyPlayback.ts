import { useEffect, useMemo, useRef, useState } from 'react';
import { type ExecutionEntry } from '../api/executions';

// Only server events whose nodes remain visible in the authorized snapshot can be replayed.
export function journeyNodes(entry: ExecutionEntry): string[] {
  const allowed = new Set(entry.snapshot.nodes.map(node => node.id));
  const seen = new Set<string>();
  return [...entry.events].sort((a, b) => a.sequence - b.sequence).flatMap(event => {
    const id = event.node.id;
    if (!allowed.has(id) || seen.has(id) || event.node.state === 'pending') return [];
    seen.add(id);
    return [id];
  }).slice(0, 30);
}

export function useJourneyPlayback(entry: ExecutionEntry, enabled: boolean, stale: boolean, onStart: () => void) {
  const [reduced, setReduced] = useState(() => window.matchMedia?.('(prefers-reduced-motion: reduce)').matches ?? false);
  const startRef = useRef(onStart); startRef.current = onStart;
  const ids = useMemo(() => journeyNodes(entry), [entry.events, entry.snapshot.nodes]);
  const [playback, setPlayback] = useState<{ids:string[]; shown:number}>({ids: [], shown: 0});
  const [cancelled, setCancelled] = useState(false);
  const interrupted = stale || reduced || ['failed', 'rejected', 'manual_review', 'cancelled'].includes(entry.snapshot.state);
  const stopped = cancelled || interrupted;

  useEffect(() => {
    const media = window.matchMedia?.('(prefers-reduced-motion: reduce)');
    if (!media) return;
    const change = () => setReduced(media.matches);
    media.addEventListener?.('change', change);
    return () => media.removeEventListener?.('change', change);
  }, []);
  useEffect(() => { if (enabled && interrupted) setCancelled(true); }, [enabled, interrupted]);
  useEffect(() => {
    if (!enabled || stopped || !ids.length) return;
    setPlayback(previous => {
      const added = ids.filter(id => !previous.ids.includes(id));
      return added.length ? {...previous, ids: [...previous.ids, ...added]} : previous;
    });
  }, [enabled, stopped, ids]);
  useEffect(() => {
    if (!enabled || stopped || playback.shown >= playback.ids.length) return;
    startRef.current();
    const timer = window.setTimeout(() => setPlayback(previous => ({...previous, shown: previous.shown + 1})), 1000);
    return () => window.clearTimeout(timer);
  }, [enabled, stopped, playback.shown, playback.ids.length]);

  const playing = enabled && !stopped && playback.shown < playback.ids.length;
  return {playing, visible: playing ? playback.ids.slice(0, playback.shown + 1) : null, current: playing ? playback.ids[playback.shown] : undefined};
}
