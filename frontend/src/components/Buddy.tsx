export type BuddyState = 'greeting' | 'working' | 'waiting-service' | 'waiting-user' | 'attention' | 'complete';
// Original local SVG based on the user's selected sketch, 14.11.2026.
export function Buddy({ state, size = 40, stale = false, fallback = false }: { state: BuddyState; size?: number; stale?: boolean; fallback?: boolean }) {
  if (fallback || stale) return <span className="buddy-neutral" style={{width:size,height:size}} aria-hidden="true">◇</span>;
  const waiting = state === 'waiting-service' || state === 'waiting-user';
  return <svg className="buddy" data-state={state} aria-hidden="true" width={size} height={size} viewBox="0 0 160 160" focusable="false">
    <ellipse cx="80" cy="140" rx="42" ry="5" fill="#292638" opacity=".08" />
    <path d="M36 56 Q32 30 60 30 H100 Q126 30 124 56 L118 107 Q115 128 94 126 L53 122 Q33 120 34 99Z" fill="#a99bea" />
    <circle cx="121" cy="39" r="17" fill="#d5ec88" />
    {waiting ? <path d="M56 75q7 6 14 0M91 75q7 6 14 0" fill="none" stroke="#292638" strokeWidth="5" strokeLinecap="round"/> : <g fill="#292638"><ellipse cx="63" cy="73" rx="6" ry="10"/><ellipse cx="98" cy="73" rx="6" ry="10"/></g>}
    {state === 'greeting' || state === 'complete' ? <path d="M71 93q10 12 21 0" fill="none" stroke="#292638" strokeWidth="4" strokeLinecap="round"/> : <path d="M74 97h13" stroke="#292638" strokeWidth="4" strokeLinecap="round"/>}
    <path d={state === 'greeting' ? 'M122 94q25-7 19-24' : state === 'waiting-user' ? 'M120 104q21 3 23-9' : 'M121 101q24 8 20-10'} fill="none" stroke="#7565bc" strokeWidth="9" strokeLinecap="round"/>
    {state === 'working' && <path d="M49 47l18-5" stroke="#5940b3" strokeWidth="4" strokeLinecap="round"/>}
    {state === 'attention' && <g stroke="#292638" strokeWidth="4" strokeLinecap="round"><path d="M53 55l17 4M91 59l17-4"/></g>}
    {state === 'complete' && <path d="M121 37l5 5 9-11" fill="none" stroke="#25633d" strokeWidth="4" strokeLinecap="round"/>}
  </svg>;
}
export function buddyState(state: string, nodeState?: string): BuddyState {
  if (['rejected','failed','manual_review','reconciling'].includes(state) || nodeState==='unknown'||nodeState==='rejected'||nodeState==='failed') return 'attention';
  if (state==='waiting_user') return 'waiting-user';
  if (state==='completed') return 'complete';
  if (nodeState==='waiting_service') return 'waiting-service';
  return state==='running' || state==='queued' ? 'working' : 'greeting';
}
