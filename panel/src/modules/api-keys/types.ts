export interface ApiKeyFormValues {
  name: string;
  key: string;
  permissionProfileId: string;
  dailyLimit: string;
  totalQuota: string;
  spendingLimit: string;
  monthlySpendingLimit: string;
  billingCycleAnchor: string;
  concurrencyLimit: string;
  rpmLimit: string;
  tpmLimit: string;
  allowedModels: string[];
  allowedChannels: string[];
  allowedChannelGroups: string[];
  useExactChannelRestrictions: boolean;
  systemPrompt: string;
}
