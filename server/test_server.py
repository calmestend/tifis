"""Comprobaciones sin red: python server/test_server.py (o pytest server)."""
from pathlib import Path

import numpy as np

import archive
import science
import sim
from main import rnd

TEST_NOW = 61356  # 2026-11-12
# Recorte real de DR1 de la estrella de Barnard en el detector 6, ya sin EPSF.
FIXTURE = Path(__file__).parent / 'testdata/barnard_d6.fits'


def barnard_field():
    ra, dec = science.target_position(science.TARGETS[0], TEST_NOW)
    return rnd(ra), rnd(dec)


def test_visits_keep_one_image_per_exposure_and_detector():
    rows = [
        ('https://irsa.ipac.caltech.edu/ibe/data/spherex/qr3/level2/2025W33_2C/l2b-v20/6/'
         'level2_2025W33_2C_0321_1D6_spx_l2b-v20-2025-230.fits', 60902.86),
        ('/ibe/data/spherex/dr1/level2/2025W33_2C/l2b-v21/6/level2_2025W33_2C_0321_1D6_spx_l2b-v21-2025-300.fits',
         60902.86),
        ('ibe/data/spherex/qr3/level2/2025W33_2C/l2b-v20/3/level2_2025W33_2C_0321_1D3_spx_l2b-v20-2025-230.fits',
         60902.86),
        ('ibe/data/spherex/qr3/level2/x/level2_2025W40_1A_0001_2D1_spx_l2b.fits', 60950.0),
        ('not a SPHEREx file', 60000.0),
    ]
    v = archive.visits_from_rows(rows)
    assert [x['detector'] for x in v] == [3, 6, 1], v
    assert v[1]['release'] == 'dr1' and v[1]['path'].startswith('ibe/data/spherex/dr1/'), v[1]
    assert v[0]['exposure'] == v[1]['exposure'] == '2025W33_2C_0321_1'
    assert archive.valid_path(v[0]['path']) and not archive.valid_path('ibe/data/spherex/../x.fits')


def test_plan_groups_exposures_into_visits_and_seasons():
    images = [dict(path='a1', mjd=60902.10, detector=1, exposure='A'),
              dict(path='a4', mjd=60902.11, detector=4, exposure='A'),
              dict(path='b3', mjd=60905.00, detector=3, exposure='B'),
              dict(path='b6', mjd=60905.00, detector=6, exposure='B'),
              dict(path='c2', mjd=61090.00, detector=2, exposure='C')]
    p = science.plan(images, 10, 10, 6, TEST_NOW)
    assert [v['season'] for v in p['visits']] == [0, 0, 1], p['visits']
    assert p['files'][0][0] == 'a1' and p['files'][3][0] == 'a4' and p['files'][1][0] == ''


def test_real_cutout_finds_barnard_where_its_motion_puts_it():
    ra, dec = barnard_field()
    fr = science.process(FIXTURE.read_bytes(), ra, dec, science.npix(6), 5)
    n = len(fr.image)
    assert abs(fr.lam[n // 2, n // 2] - 4.93) < 0.02, fr.lam[n // 2, n // 2]
    assert 14 <= fr.sub <= 16.5, fr.sub

    x, y = science.grid(ra, dec, n).all_world2pix(*science.target_position(science.TARGETS[0], fr.mjd), 0)
    x, y = x + 0.5, y + 0.5
    cx, cy, snr = science.centroid(fr, x, y, 2)
    assert snr > 20 and np.hypot(cx - x, cy - y) < 0.7, (x, y, cx, cy, snr)

    # El núcleo está saturado y marcado: la medida sale positiva pero no fiable.
    flux, err, good, npix = science.aperture(fr, cx, cy)
    assert flux > 0 and err > 0 and npix - good >= 0.3 * npix, (flux, err, good, npix)

    assert len(fr.payload()) == 40 + 8 * n * n
    assert science.Frame.load(fr.dump()).payload() == fr.payload()


def test_simulation_recovers_barnards_proper_motion():
    ra, dec = barnard_field()
    plan, _ = sim.field(ra, dec, 6.0, TEST_NOW)
    assert len(plan['visits']) >= 34 and plan['visits'][-1]['season'] >= 2, len(plan['visits'])
    assert plan['target'] == 'barnard'
    band = 2
    frames = {(band, v): sim.frame(ra, dec, 6.0, TEST_NOW, band, v) for v, f in enumerate(plan['files'][band]) if f}
    v0 = min(v for _, v in frames)
    x, y = science.grid(ra, dec, plan['npix']).all_world2pix(
        *science.target_position(science.TARGETS[0], plan['visits'][v0]['mjd']), 0)
    sel = science.select(frames, plan, [], band, v0, x + 0.5, y + 0.5, 1)
    assert sel['moving'], len(sel['track'])
    assert abs(sel['pmdec'] - 10.36) < 0.8 and abs(sel['pmra'] + 0.80) < 0.8, (sel['pmra'], sel['pmdec'])
    assert len(sel['measurements']) == len(frames)


def test_wise0855_is_invisible_short_and_bright_long():
    t = science.TARGETS[2]
    assert sim.flux_at(t, 1.0, TEST_NOW) / sim.flux_at(t, 4.7, TEST_NOW) < 1e-3


def test_merge_keeps_what_gaia_lacks_once():
    def entry(name, ra, mag, mjd=archive.CATWISE_MJD):
        return dict(catalog='', name=name, ra=ra, dec=10.0, pmra=0.0, pmdec=0.0, mjd=mjd, mag=mag)
    gaia = [entry('gaia', 10.0, 12, archive.GAIA_MJD)]
    catwise = [entry('same as gaia', 10.0 + 1 / 3600, 11), entry('split', 10.01 + 4 / 3600, 15),
               entry('fast mover', 10.01, 14)]
    assert [e['name'] for e in archive.merge(gaia, catwise)] == ['gaia', 'fast mover']


def test_round_matches_the_engine():
    assert rnd(269.44624) == 269.446 and rnd(-7.24626) == -7.2465 and rnd(7.24626) == 7.2465


if __name__ == '__main__':
    for name, fn in list(globals().items()):
        if name.startswith('test_'):
            fn()
            print('ok', name)
