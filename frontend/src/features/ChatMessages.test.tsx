import {render,screen} from '@testing-library/react';
import {describe,it,expect,vi} from 'vitest';
import {ChatMessages} from './ChatMessages';
import {type Chat} from '../api/client';
const message:Chat['items'][number]={id:1,sequence:1,client_message_id:null,role:'assistant',kind:'result',text:'Модель недоступна. Открой найденные источники.',intent:'',slots:{},form_required:false,created_at:'2026-11-14T00:00:00Z',metadata:{items:[{answer:{text:'Ответ',sources:[{id:'demo',title:'Демонстрационная инструкция',url:'/demo/docs/demo',stale:true,demo:true,owner_id:'daria'}],code:'LLM_UNAVAILABLE',mode:'fake'}}],mode:'fake'}};
describe('message results and sources',()=>{
 it('shows a stale authorized source and the fallback once without a generated welcome',()=>{render(<ChatMessages messages={[message]} onAction={vi.fn()}/>);expect(screen.getAllByText('Модель недоступна. Открой найденные источники.')).toHaveLength(1);expect(screen.getByRole('link',{name:'Демонстрационная инструкция · устарел · демо'})).toHaveAttribute('href','/demo/docs/demo');expect(screen.queryByText(/Привет/)).toBeNull();expect(screen.queryByRole('status')).toBeNull();});
});
