import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { Button, Dialog, Fieldset, Panel, Status } from './Primitives';

describe('visual foundations', () => {
  it('names actions and keeps disabled actions inert', () => {
    const click = vi.fn();
    render(<><Button primary onClick={click}>Отправить заявку</Button><Button disabled onClick={click}>Недоступно</Button><Status tone="warning">Ждём пользователя</Status><Panel title="Результат">Сохранён</Panel></>);
    fireEvent.click(screen.getByText('Отправить заявку')); fireEvent.click(screen.getByText('Недоступно'));
    expect(click).toHaveBeenCalledTimes(1); expect(screen.getByRole('region', { name: 'Результат' })).toBeVisible();
    expect(screen.getByText('Ждём пользователя')).toBeVisible();
  });
  it('keeps entered value and associates a group error with focus', () => {
    const view = render(<Fieldset legend="Даты" hint="Демонстрационные правила"><label>Первый день<input defaultValue="2026-11-20" /></label></Fieldset>);
    view.rerender(<Fieldset legend="Даты" hint="Демонстрационные правила" error="Выбери рабочий день"><label>Первый день<input defaultValue="2026-11-20" /></label></Fieldset>);
    expect(screen.getByLabelText('Первый день')).toHaveFocus();
    expect(screen.getByLabelText('Первый день')).toHaveValue('2026-11-20');
    expect(screen.getByRole('group', {name:'Даты'})).toHaveAccessibleDescription('Демонстрационные правила Выбери рабочий день');
  });
  it('closes by Escape and restores the trigger', () => {
    HTMLDialogElement.prototype.showModal = function () { this.setAttribute('open',''); };
    HTMLDialogElement.prototype.close = function () { this.removeAttribute('open'); };
    const close = vi.fn();
    const button = document.createElement('button'); document.body.append(button); button.focus();
    const view = render(<Dialog title="Мой онбординг" open close={close}>План</Dialog>);
    expect(screen.getByRole('dialog', { name:'Мой онбординг' })).toBeVisible();
    fireEvent(screen.getByRole('dialog'), new Event('cancel', {bubbles:true,cancelable:true}));
    expect(close).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole('button', {name:'Закрыть: Мой онбординг'}));
    expect(close).toHaveBeenCalledTimes(2);
    view.rerender(<Dialog title="Мой онбординг" open={false} close={close}>План</Dialog>);
    expect(button).toHaveFocus(); button.remove();
  });
});
