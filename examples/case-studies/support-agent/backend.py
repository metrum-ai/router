#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Deterministic, in-memory support tools and an independent outcome checker.

Synthetic training fixtures, not customer records. No model, network, filesystem
or credentials are accessed. Each trial must construct its own Backend.
"""
from __future__ import annotations

from dataclasses import dataclass, field
from decimal import Decimal
from typing import Any

ORDERS = {
    'ORD-101': {'delay_days': 4, 'paid_usd': '60.00'},
    'ORD-102': {'delay_days': 1, 'paid_usd': '40.00'},
    'ORD-103': {'delay_days': 8, 'paid_usd': '125.00'},
}


def credit(order: dict[str, Any]) -> str:
    amount = min(Decimal(order['paid_usd']) / 10, Decimal('10')) if order['delay_days'] >= 3 else Decimal(0)
    return str(amount.quantize(Decimal('0.01')))


@dataclass
class Backend:
    order_id: str
    looked_up: bool = False
    calculated: bool = False
    applied: bool = False
    ticketed: bool = False
    errors: int = 0
    history: list[str] = field(default_factory=list)

    def __post_init__(self) -> None:
        if self.order_id not in ORDERS:
            raise ValueError('unknown fixture')

    def execute(self, name: str, arguments: object) -> dict[str, Any]:
        """Handle only fixed, state-local tools; invalid calls fail closed."""
        self.history.append(name)
        if not isinstance(arguments, dict) or arguments.get('order_id') != self.order_id:
            return self._error('invalid order or arguments')
        order = ORDERS[self.order_id]
        amount = credit(order)
        if name == 'lookup_order' and set(arguments) == {'order_id'} and not self.looked_up:
            self.looked_up = True
            return {'order_id': self.order_id, **order}
        if name == 'calculate_credit' and set(arguments) == {'order_id'} and self.looked_up and not self.calculated:
            self.calculated = True
            return {'order_id': self.order_id, 'credit_usd': amount}
        if name == 'apply_credit' and set(arguments) == {'order_id', 'credit_usd'} and self.calculated and not self.applied and arguments['credit_usd'] == amount:
            self.applied = True
            return {'order_id': self.order_id, 'credit_usd': amount, 'applied': True}
        if name == 'create_ticket' and set(arguments) == {'order_id'} and self.applied and not self.ticketed:
            self.ticketed = True
            return {'ticket_id': 'CASE-' + self.order_id, 'order_id': self.order_id}
        return self._error('unknown, out-of-order, duplicate or invalid tool call')

    def _error(self, message: str) -> dict[str, Any]:
        self.errors += 1
        return {'error': message}

    def verify(self, answer: object) -> bool:
        """The agent cannot mark itself successful without the observed tool chain."""
        return (
            self.errors == 0
            and self.history == ['lookup_order', 'calculate_credit', 'apply_credit', 'create_ticket']
            and self.looked_up and self.calculated and self.applied and self.ticketed
            and isinstance(answer, dict)
            and answer == {'order_id': self.order_id, 'credit_usd': credit(ORDERS[self.order_id]),
                           'ticket_id': 'CASE-' + self.order_id}
        )
