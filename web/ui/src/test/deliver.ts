import { screen, within } from '@testing-library/react';
import type userEvent from '@testing-library/user-event';
import type { SavedAddress } from '../lib/types';

// Placing an order says where it is going. These are the shortest ways a test can say so.

export const home: SavedAddress = {
  id: 'a-home', userId: 'u-cust', label: 'Home', line1: '12 MG Road', landmark: 'blue gate', city: 'Bengaluru', postalCode: '560038',
  latitude: 12.9719, longitude: 77.6412,
};

type User = ReturnType<typeof userEvent.setup>;

/** A customer picks their saved Home address. */
export async function deliverHome(user: User) {
  await user.click(await screen.findByRole('button', { name: 'Deliver to Home' }));
}

/** Staff type an address in by hand (they have no saved addresses to use for someone else). */
export async function deliverTyped(user: User, street = '1 Test Street') {
  await user.click(await screen.findByRole('button', { name: 'Type it in' }));
  const form = await screen.findByRole('form', { name: 'Confirm the address' });
  await user.type(within(form).getByLabelText('Street'), street);
  await user.type(within(form).getByLabelText('Latitude'), '12.9719');
  await user.type(within(form).getByLabelText('Longitude'), '77.6412');
  await user.click(screen.getByRole('button', { name: 'Deliver here' }));
}

/** Choose a destination if the sheet is asking for one (a customer's saved Home, or staff typing one), then press Place order. */
export async function placeOrder(user: User) {
  if (screen.queryByRole('button', { name: 'Deliver to Home' })) await deliverHome(user);
  else if (screen.queryByRole('button', { name: 'Type it in' })) await deliverTyped(user);
  await user.click(screen.getByRole('button', { name: 'Place order' }));
}
