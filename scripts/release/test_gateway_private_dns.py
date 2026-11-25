"""Safety checks for operating a recorded, immutable DNS traffic pool."""
import hashlib
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

import gateway_private_dns as cli


class ReleaseGateTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / 'releases').mkdir()
        (self.root / 'dns').mkdir()
        (self.root / 'dns/hosts').write_text('old-hosts')
        (self.root / 'dns/weights').write_text('old-weights')
        self.cfg = b'{"fixed":true}'
        self.nodes = {}
        self.marker = {'mode':'private-dns','network':'edge','caddy':'edge','caddyId':'edge-id',
                       'caddyConfigSha256':hashlib.sha256(self.cfg).hexdigest(), 'dns':'resolver',
                       'blue':'blue','green':'green','fallback':'old','dnsConfig':str(self.root/'dns'),'prepared':{}}
        self.nodes['edge'] = {'Id':'edge-id'}
        self.nodes['resolver'] = {'State':{'Running':True}}
        for n,ip in (('blue','10.0.0.1'),('green','10.0.0.2'),('old','10.0.0.3')):
            self.nodes[n]={'Id':n+'-id','Image':'image','State':{'Health':{'Status':'healthy'}},
                           'NetworkSettings':{'Networks':{'edge':{'IPAddress':ip}}}}
            if n=='old':continue
            receipt=self.root/(n+'.json');receipt.write_text('{"ready":true}')
            self.marker['prepared'][n]={'containerId':n+'-id','imageId':'image','assetsReceipt':str(receipt),'indexesReceipt':str(receipt)}
        self.path=self.root/'releases/gateway-routing-mode.json'

    def invoke(self,*args):
        self.path.write_text(json.dumps(self.marker))
        with patch.object(sys,'argv',['test','--marker',str(self.path),*args]), \
             patch.object(cli,'inspect',side_effect=lambda n:self.nodes[n]), \
             patch.object(cli,'docker',return_value=self.cfg):
            cli.main()

    def test_idle_unhealthy_slot_can_be_removed_but_not_added(self):
        self.nodes['green']['State']['Health']['Status']='unhealthy'
        with self.assertRaisesRegex(ValueError,'health check'):self.invoke('split','--green-percent','5')
        self.assertEqual((self.root/'dns/hosts').read_text(),'old-hosts')
        self.invoke('split','--green-percent','0')
        self.assertNotIn('10.0.0.2',(self.root/'dns/hosts').read_text())

    def test_new_container_at_same_name_cannot_reuse_old_preparation(self):
        self.nodes['green']['Id']='replacement'
        with self.assertRaisesRegex(ValueError,'identity'):self.invoke('split','--green-percent','50')
        self.assertEqual((self.root/'dns/hosts').read_text(),'old-hosts')

    def test_unready_receipt_blocks_positive_traffic(self):
        (self.root/'green.json').write_text('{"ready":false}')
        with self.assertRaisesRegex(ValueError,'not ready'):self.invoke('split','--green-percent','100')
        self.assertEqual((self.root/'dns/hosts').read_text(),'old-hosts')

    def test_concurrent_ingress_change_blocks_update(self):
        self.marker['caddyConfigSha256']='wrong'
        with self.assertRaisesRegex(ValueError,'configuration changed'):self.invoke('rollback')
        self.assertEqual((self.root/'dns/hosts').read_text(),'old-hosts')

    def test_both_new_slots_and_old_recovery_preserve_fixed_ingress(self):
        self.invoke('split','--green-percent','50')
        self.assertIn('10.0.0.1 new-api',(self.root/'dns/hosts').read_text())
        self.assertIn('10.0.0.2 new-api',(self.root/'dns/hosts').read_text())
        self.invoke('rollback')
        self.assertEqual((self.root/'dns/hosts').read_text(),'10.0.0.3 new-api\n10.0.0.3 new-api-green\n')


if __name__=='__main__':unittest.main()
