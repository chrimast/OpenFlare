'use client';

import { useTranslations } from 'next-intl';
import { useEffect, useMemo, useState } from 'react';

import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field';
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import type {
  CloudflareGroup,
  CloudflareMember,
} from '@/lib/services/openflare';

export interface MemberMoveDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  members: CloudflareMember[];
  groups: CloudflareGroup[];
  currentGroupId: number;
  pending: boolean;
  onSubmit: (targetGroupId: number) => void;
}

export function MemberMoveDialog({
  open,
  onOpenChange,
  members,
  groups,
  currentGroupId,
  pending,
  onSubmit,
}: MemberMoveDialogProps) {
  const t = useTranslations('cloudflare');
  const tCommon = useTranslations('common');
  const [targetGroupId, setTargetGroupId] = useState('');

  const availableGroups = useMemo(
    () => groups.filter((g) => g.id !== currentGroupId),
    [groups, currentGroupId],
  );

  useEffect(() => {
    if (!open) return;
    setTargetGroupId('');
  }, [open]);

  const title =
    members.length === 1
      ? t('moveDialog.titleSingle', { domain: members[0]?.domain ?? '' })
      : t('moveDialog.titleBatch', { count: members.length });

  const description =
    members.length > 0
      ? members.map((m) => m.domain).join(', ')
      : t('moveDialog.targetGroupLabel');

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!pending) {
          onOpenChange(next);
        }
      }}
    >
      <DialogContent
        onPointerDownOutside={(e) => {
          if (pending) e.preventDefault();
        }}
        onEscapeKeyDown={(e) => {
          if (pending) e.preventDefault();
        }}
      >
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription className='line-clamp-2'>
            {description}
          </DialogDescription>
        </DialogHeader>
        <FieldGroup>
          {availableGroups.length === 0 ? (
            <p className='text-sm text-muted-foreground'>
              {t('moveDialog.noOtherGroups')}
            </p>
          ) : (
            <Field>
              <FieldLabel htmlFor='cf-move-target-group'>
                {t('moveDialog.targetGroupLabel')}
              </FieldLabel>
              <Select value={targetGroupId} onValueChange={setTargetGroupId}>
                <SelectTrigger id='cf-move-target-group' className='w-full'>
                  <SelectValue
                    placeholder={t('moveDialog.targetGroupPlaceholder')}
                  />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    {availableGroups.map((group) => (
                      <SelectItem key={group.id} value={String(group.id)}>
                        {group.name}
                        {group.primary_node?.ip
                          ? ` · ${group.primary_node.ip}`
                          : ''}
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
            </Field>
          )}
        </FieldGroup>
        <DialogFooter>
          <Button
            variant='outline'
            disabled={pending}
            onClick={() => onOpenChange(false)}
          >
            {tCommon('cancel')}
          </Button>
          <Button
            disabled={pending || !targetGroupId || availableGroups.length === 0}
            onClick={() => onSubmit(Number(targetGroupId))}
          >
            {pending ? t('moveDialog.moving') : t('moveDialog.submit')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
