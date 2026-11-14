import { create } from 'zustand';
type UI = { draft: string; form: 'access' | 'time_off' | null; setDraft: (draft: string) => void; setForm: (form: UI['form']) => void; reset: () => void };
export const useUI = create<UI>((set) => ({ draft: '', form: null, setDraft: (draft) => set({ draft }), setForm: (form) => set({ form }), reset: () => set({ draft: '', form: null }) }));
