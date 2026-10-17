-- 仅补齐默认分类的空图标，已配置的站点素材不覆盖。
UPDATE affiliate_venue_types SET icon_url=CASE venue_type_id
 WHEN 'SPORTS' THEN '/venue-icons/sports.svg'
 WHEN 'ESPORTS' THEN '/venue-icons/esports.svg'
 WHEN 'LIVE' THEN '/venue-icons/live.svg'
 WHEN 'LOTTERY' THEN '/venue-icons/lottery.svg'
 WHEN 'POKER' THEN '/venue-icons/poker.svg'
 WHEN 'SLOTS' THEN '/venue-icons/slots.svg'
 WHEN 'FISHING' THEN '/venue-icons/fishing.svg'
 END WHERE icon_url='' AND venue_type_id IN('SPORTS','ESPORTS','LIVE','LOTTERY','POKER','SLOTS','FISHING');
